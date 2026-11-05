package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mockLimiterBlocked struct {
	retryAfter int64
}

func (m *mockLimiterBlocked) Allow(ctx context.Context, identity string) (RateLimitResult, error) {
	return RateLimitResult{
		Allowed:    false,
		Limit:      10,
		Remaining:  0,
		RetryAfter: m.retryAfter,
	}, nil
}

func TestBackendPoolWeightedRoundRobinAndHealthFiltering(t *testing.T) {
	nodes := make([]*BackendNode, 0, 2)
	for i := 0; i < 2; i++ {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		t.Cleanup(server.Close)
		target, err := url.Parse(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, NewBackendNode(target, nil, http.DefaultTransport))
	}
	if err := nodes[0].SetWeight(2); err != nil {
		t.Fatal(err)
	}
	if err := nodes[1].SetWeight(1); err != nil {
		t.Fatal(err)
	}
	pool, err := NewBackendPool(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.SetStrategy(StrategyWeightedRoundRobin); err != nil {
		t.Fatal(err)
	}

	counts := map[string]int{}
	for i := 0; i < 30; i++ {
		counts[pool.SelectNode().Name()]++
	}
	if counts[nodes[0].Name()] != 20 || counts[nodes[1].Name()] != 10 {
		t.Fatalf("weighted distribution = %v, want 20:10", counts)
	}

	nodes[0].RecordHealthFailure(errors.New("probe failed"), 1)
	for i := 0; i < 20; i++ {
		if selected := pool.SelectNode(); selected != nodes[1] {
			t.Fatalf("selected unhealthy node %s", selected.Name())
		}
	}
}

func TestBackendPoolLeastConnectionsRandomAndWeights(t *testing.T) {
	var nodes []*BackendNode
	for i := 0; i < 3; i++ {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		t.Cleanup(server.Close)
		target, err := url.Parse(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, NewBackendNode(target, nil, http.DefaultTransport))
	}
	pool, err := NewBackendPool(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.SetStrategy(StrategyLeastConnections); err != nil {
		t.Fatal(err)
	}
	nodes[0].activeRequests.Store(4)
	nodes[1].activeRequests.Store(2)
	if selected := pool.SelectNode(); selected != nodes[2] {
		t.Fatalf("least connections selected %s, want node with zero active requests", selected.Name())
	}

	if err := pool.SetStrategy(StrategyRandom); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if selected := pool.SelectNode(); selected == nil {
			t.Fatal("random strategy returned a nil backend")
		}
	}
	if err := pool.SetStrategy("unsupported"); err == nil {
		t.Fatal("expected unknown balancing strategy to be rejected")
	}

	if err := applyBackendWeights(pool, "2,1,3"); err != nil {
		t.Fatalf("apply backend weights: %v", err)
	}
	if got := []int64{nodes[0].Weight(), nodes[1].Weight(), nodes[2].Weight()}; got[0] != 2 || got[1] != 1 || got[2] != 3 {
		t.Fatalf("configured backend weights = %v", got)
	}
	if err := applyBackendWeights(pool, "2,0,3"); err == nil {
		t.Fatal("expected non-positive backend weight to be rejected")
	}
}

func TestRequestID_GeneratedAndReturnedInHeader(t *testing.T) {
	limiter := &mockLimiterAllowed{}
	handler, _ := setupTestGateway(t, limiter)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	reqID := rr.Header().Get("X-Request-ID")
	if reqID == "" {
		t.Fatal("expected X-Request-ID header on response, got empty")
	}
	if len(reqID) < 10 {
		t.Fatalf("expected substantial Request ID, got %q", reqID)
	}
}

func TestRequestID_ClientProvidedValidPreserved(t *testing.T) {
	limiter := &mockLimiterAllowed{}
	handler, _ := setupTestGateway(t, limiter)

	customID := "client-trace-id-abc-123_456"
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("X-Request-ID", customID)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("X-Request-ID"); got != customID {
		t.Fatalf("expected client X-Request-ID %q preserved, got %q", customID, got)
	}
}

func TestRequestID_UpstreamCannotReplaceGatewayID(t *testing.T) {
	logger := NewRequestLogger(10)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "upstream-id")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	node := NewBackendNode(target, nil, http.DefaultTransport)
	pool, err := NewBackendPool([]*BackendNode{node})
	if err != nil {
		t.Fatal(err)
	}
	handler := newGatewayHandlerWithOptions(
		testAPIKey,
		pool,
		&mockLimiterAllowed{},
		WithRequestLogger(logger),
	)
	req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req.Header.Set("X-API-Key", testAPIKey)
	req.Header.Set("X-Request-ID", "gateway-request-123")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.Code)
	}
	if got := res.Header().Get("X-Request-ID"); got != "gateway-request-123" {
		t.Fatalf("response request ID = %q, want gateway-request-123", got)
	}
	entries := logger.GetEntries(10, 0, "", "")
	if len(entries) != 1 || entries[0].ID != "gateway-request-123" {
		t.Fatalf("request log does not match response ID: %+v", entries)
	}
}

func TestRequestID_ClientProvidedInvalidSanitized(t *testing.T) {
	limiter := &mockLimiterAllowed{}
	handler, _ := setupTestGateway(t, limiter)

	// Inject characters that are unsafe or outside alphanumeric/dash/underscore bounds
	invalidID := "<script>alert(1)</script>"
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("X-Request-ID", invalidID)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	got := rr.Header().Get("X-Request-ID")
	if got == invalidID {
		t.Fatalf("expected invalid X-Request-ID to be replaced, but it was preserved: %q", got)
	}
	if strings.Contains(got, "<") || strings.Contains(got, ">") {
		t.Fatalf("sanitized ID contains forbidden chars: %q", got)
	}
}

func TestRequestID_UniqueUnderConcurrency(t *testing.T) {
	limiter := &mockLimiterAllowed{}
	handler, _ := setupTestGateway(t, limiter)

	const concurrency = 100
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := make(map[string]bool)

	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			id := rr.Header().Get("X-Request-ID")
			if id == "" {
				t.Errorf("empty request id")
				return
			}
			mu.Lock()
			if seen[id] {
				t.Errorf("duplicate request id under concurrency: %s", id)
			}
			seen[id] = true
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(seen) != concurrency {
		t.Fatalf("expected %d unique request IDs, got %d", concurrency, len(seen))
	}
}

func TestRequestLogger_LogsSuccessfulAndFailedRequests(t *testing.T) {
	logger := NewRequestLogger(50)
	limiter := &mockLimiterAllowed{}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend-Id", "backend-alpha")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	node := NewBackendNode(u, nil, http.DefaultTransport)
	pool, _ := NewBackendPool([]*BackendNode{node})

	handler := newGatewayHandlerWithOptions(
		testAPIKey,
		pool,
		limiter,
		WithRequestLogger(logger),
	)

	// 1. Successful request
	req1 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req1.Header.Set("X-API-Key", testAPIKey)
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, req1)

	if rr1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr1.Code)
	}

	entries := logger.GetEntries(10, 0, "", "")
	if len(entries) < 1 {
		t.Fatalf("expected at least 1 log entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.Status != http.StatusOK {
		t.Errorf("expected entry status 200, got %d", entry.Status)
	}
	if entry.Method != "GET" {
		t.Errorf("expected entry method GET, got %s", entry.Method)
	}
	if entry.Path != "/api/students" {
		t.Errorf("expected entry path /api/students, got %s", entry.Path)
	}
	if entry.Backend != "backend-alpha" {
		t.Errorf("expected entry backend backend-alpha, got %s", entry.Backend)
	}
	if entry.FailureCategory != "" {
		t.Errorf("expected empty failure category for 200 OK, got %s", entry.FailureCategory)
	}
}

func TestRequestLogger_AuthenticationFailureLogged(t *testing.T) {
	logger := NewRequestLogger(50)
	limiter := &mockLimiterAllowed{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	node := NewBackendNode(u, nil, http.DefaultTransport)
	pool, _ := NewBackendPool([]*BackendNode{node})

	handler := newGatewayHandlerWithOptions(
		testAPIKey,
		pool,
		limiter,
		WithRequestLogger(logger),
	)

	// Send unauthorized request (missing key)
	req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}

	entries := logger.GetEntries(10, 0, "", "")
	if len(entries) < 1 {
		t.Fatalf("expected logged 401 entry")
	}

	entry := entries[0]
	if entry.Status != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", entry.Status)
	}
	if entry.FailureCategory != "auth_failed" {
		t.Errorf("expected failureCategory auth_failed, got %q", entry.FailureCategory)
	}
}

func TestRequestLogger_RateLimitFailureLogged(t *testing.T) {
	logger := NewRequestLogger(50)
	limiter := &mockLimiterBlocked{retryAfter: 30}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	node := NewBackendNode(u, nil, http.DefaultTransport)
	pool, _ := NewBackendPool([]*BackendNode{node})

	handler := newGatewayHandlerWithOptions(
		testAPIKey,
		pool,
		limiter,
		WithRequestLogger(logger),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req.Header.Set("X-API-Key", testAPIKey)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rr.Code)
	}

	entries := logger.GetEntries(10, 0, "", "")
	if len(entries) < 1 {
		t.Fatalf("expected logged 429 entry")
	}

	entry := entries[0]
	if entry.Status != http.StatusTooManyRequests {
		t.Errorf("expected status 429, got %d", entry.Status)
	}
	if entry.FailureCategory != "rate_limited" {
		t.Errorf("expected failureCategory rate_limited, got %q", entry.FailureCategory)
	}
}

func TestRequestLogger_SensitiveDataNotLogged(t *testing.T) {
	logger := NewRequestLogger(50)
	limiter := &mockLimiterAllowed{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	node := NewBackendNode(u, nil, http.DefaultTransport)
	pool, _ := NewBackendPool([]*BackendNode{node})

	handler := newGatewayHandlerWithOptions(
		testAPIKey,
		pool,
		limiter,
		WithRequestLogger(logger),
	)

	// Send request with sensitive query parameter
	req := httptest.NewRequest(http.MethodGet, "/api/students?secret_token=super-secret-12345", nil)
	req.Header.Set("X-API-Key", testAPIKey)
	req.Header.Set("Authorization", "Bearer sensitive-auth-token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	entries := logger.GetEntries(10, 0, "", "")
	if len(entries) == 0 {
		t.Fatal("expected log entry")
	}

	entry := entries[0]
	// Verify raw query params are not logged in path
	if strings.Contains(entry.Path, "secret_token") || strings.Contains(entry.Path, "super-secret") {
		t.Fatalf("sensitive query parameter found in logged path: %s", entry.Path)
	}
	if entry.Path != "/api/students" {
		t.Fatalf("expected clean path /api/students, got %s", entry.Path)
	}

	// Verify JSON serialization does not contain sensitive keys
	b, _ := json.Marshal(entry)
	s := string(b)
	if strings.Contains(s, testAPIKey) || strings.Contains(s, "sensitive-auth-token") {
		t.Fatalf("log entry serialized to JSON leaked sensitive token: %s", s)
	}
}

func TestRequestLogger_StorageRemainsBounded(t *testing.T) {
	const capacity = 5
	logger := NewRequestLogger(capacity)

	for i := 1; i <= 20; i++ {
		logger.Record(RequestLogEntry{
			ID:     fmt.Sprintf("req-%d", i),
			Method: "GET",
			Path:   "/api/test",
			Status: 200,
		})
	}

	if count := logger.TotalCount(); count != capacity {
		t.Fatalf("expected bounded count of %d, got %d", capacity, count)
	}

	entries := logger.GetEntries(100, 0, "", "")
	if len(entries) != capacity {
		t.Fatalf("expected exactly %d entries returned, got %d", capacity, len(entries))
	}

	// Newest entry should be req-20
	if entries[0].ID != "req-20" {
		t.Fatalf("expected newest entry to be req-20, got %s", entries[0].ID)
	}
	// Oldest stored entry should be req-16 (since 20 - 5 + 1 = 16)
	if entries[capacity-1].ID != "req-16" {
		t.Fatalf("expected oldest stored entry to be req-16, got %s", entries[capacity-1].ID)
	}
}

func TestRequestInspector_EndpointFiltersAndAuth(t *testing.T) {
	logger := NewRequestLogger(50)
	logger.Record(RequestLogEntry{ID: "req-success-1", Method: "GET", Path: "/api/students", Status: 200})
	logger.Record(RequestLogEntry{ID: "req-auth-fail", Method: "GET", Path: "/api/students", Status: 401, FailureCategory: "auth_failed"})
	logger.Record(RequestLogEntry{ID: "req-post-rate", Method: "POST", Path: "/api/orders", Status: 429, FailureCategory: "rate_limited"})

	handler := newGatewayHandlerWithOptions(
		testAPIKey,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		&mockLimiterAllowed{},
		WithRequestLogger(logger),
	)

	// 1. Unauthenticated request to /api/requests -> 401
	reqUnauth := httptest.NewRequest(http.MethodGet, "/api/requests", nil)
	rrUnauth := httptest.NewRecorder()
	handler.ServeHTTP(rrUnauth, reqUnauth)
	if rrUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated /api/requests, got %d", rrUnauth.Code)
	}

	// 2. Authenticated request -> 200
	reqAuth := httptest.NewRequest(http.MethodGet, "/api/requests", nil)
	reqAuth.Header.Set("X-API-Key", testAPIKey)
	rrAuth := httptest.NewRecorder()
	handler.ServeHTTP(rrAuth, reqAuth)
	if rrAuth.Code != http.StatusOK {
		t.Fatalf("expected 200 for authenticated /api/requests, got %d", rrAuth.Code)
	}

	var resp struct {
		Requests []RequestLogEntry `json:"requests"`
		Count    int               `json:"count"`
	}
	if err := json.Unmarshal(rrAuth.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if resp.Count < 3 {
		t.Fatalf("expected at least 3 entries, got %d", resp.Count)
	}

	// 3. Status filter (status=401)
	reqFilterStatus := httptest.NewRequest(http.MethodGet, "/api/requests?status=401", nil)
	reqFilterStatus.Header.Set("X-API-Key", testAPIKey)
	rrFilterStatus := httptest.NewRecorder()
	handler.ServeHTTP(rrFilterStatus, reqFilterStatus)

	var statusResp struct {
		Requests []RequestLogEntry `json:"requests"`
	}
	_ = json.Unmarshal(rrFilterStatus.Body.Bytes(), &statusResp)
	if len(statusResp.Requests) != 1 || statusResp.Requests[0].Status != 401 {
		t.Fatalf("expected 1 entry with status 401, got %+v", statusResp.Requests)
	}

	// 4. Method filter (method=POST)
	reqFilterMethod := httptest.NewRequest(http.MethodGet, "/api/requests?method=POST", nil)
	reqFilterMethod.Header.Set("X-API-Key", testAPIKey)
	rrFilterMethod := httptest.NewRecorder()
	handler.ServeHTTP(rrFilterMethod, reqFilterMethod)

	var methodResp struct {
		Requests []RequestLogEntry `json:"requests"`
	}
	_ = json.Unmarshal(rrFilterMethod.Body.Bytes(), &methodResp)
	if len(methodResp.Requests) != 1 || methodResp.Requests[0].Method != "POST" {
		t.Fatalf("expected 1 entry with method POST, got %+v", methodResp.Requests)
	}

	// 5. Search filter (search=req-auth-fail)
	reqFilterSearch := httptest.NewRequest(http.MethodGet, "/api/requests?search=auth-fail", nil)
	reqFilterSearch.Header.Set("X-API-Key", testAPIKey)
	rrFilterSearch := httptest.NewRecorder()
	handler.ServeHTTP(rrFilterSearch, reqFilterSearch)

	var searchResp struct {
		Requests []RequestLogEntry `json:"requests"`
	}
	_ = json.Unmarshal(rrFilterSearch.Body.Bytes(), &searchResp)
	if len(searchResp.Requests) != 1 || searchResp.Requests[0].ID != "req-auth-fail" {
		t.Fatalf("expected entry with ID req-auth-fail, got %+v", searchResp.Requests)
	}
}

func TestBackendHealth_HealthyRemainsAvailable(t *testing.T) {
	u, _ := url.Parse("http://localhost:8081")
	node := NewBackendNode(u, nil, http.DefaultTransport)

	if node.HealthState() != BackendHealthHealthy {
		t.Fatalf("expected newly created node to be BackendHealthHealthy, got %s", node.HealthState())
	}
	if !node.IsAvailable() {
		t.Fatal("expected healthy node to be available")
	}
}

func TestBackendHealth_ConsecutiveFailuresMarkUnhealthy(t *testing.T) {
	u, _ := url.Parse("http://localhost:8081")
	node := NewBackendNode(u, nil, http.DefaultTransport)

	unhealthyThreshold := 3
	node.RecordHealthFailure(errors.New("timeout"), unhealthyThreshold)
	if node.HealthState() != BackendHealthHealthy {
		t.Fatalf("expected still healthy after 1 failure, got %s", node.HealthState())
	}
	if node.ConsecutiveHealthFailures() != 1 {
		t.Fatalf("expected 1 consecutive failure, got %d", node.ConsecutiveHealthFailures())
	}

	node.RecordHealthFailure(errors.New("timeout"), unhealthyThreshold)
	if node.HealthState() != BackendHealthHealthy {
		t.Fatalf("expected still healthy after 2 failures, got %s", node.HealthState())
	}

	// 3rd failure reaches threshold
	node.RecordHealthFailure(errors.New("connection refused"), unhealthyThreshold)
	if node.HealthState() != BackendHealthUnhealthy {
		t.Fatalf("expected BackendHealthUnhealthy after %d failures, got %s", unhealthyThreshold, node.HealthState())
	}
	if node.IsAvailable() {
		t.Fatal("unhealthy backend must not be available for traffic")
	}
}

func TestBackendHealth_UnhealthyExcludedFromRouting(t *testing.T) {
	var count1, count2 atomic.Int64
	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count1.Add(1)
		w.Header().Set("X-Backend-Id", "node-1")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend1.Close()

	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count2.Add(1)
		w.Header().Set("X-Backend-Id", "node-2")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend2.Close()

	u1, _ := url.Parse(backend1.URL)
	u2, _ := url.Parse(backend2.URL)

	node1 := NewBackendNode(u1, nil, http.DefaultTransport)
	node2 := NewBackendNode(u2, nil, http.DefaultTransport)

	// Mark node2 unhealthy
	node2.RecordHealthFailure(errors.New("service crashed"), 1)
	if node2.HealthState() != BackendHealthUnhealthy {
		t.Fatalf("node2 not unhealthy")
	}

	pool, _ := NewBackendPool([]*BackendNode{node1, node2})
	handler := newGatewayHandler(testAPIKey, pool, &mockLimiterAllowed{})

	// Send 10 requests: All 10 must hit node1, none to node2
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rr.Code)
		}
	}

	if count1.Load() != 10 {
		t.Errorf("expected 10 requests to node1, got %d", count1.Load())
	}
	if count2.Load() != 0 {
		t.Errorf("expected 0 requests to unhealthy node2, got %d", count2.Load())
	}
}

func TestBackendHealth_RecoveringTransitionToHealthy(t *testing.T) {
	u, _ := url.Parse("http://localhost:8081")
	cb := NewCircuitBreaker(1, 10*time.Millisecond)
	node := NewBackendNode(u, cb, http.DefaultTransport)

	// Mark unhealthy
	node.RecordHealthFailure(errors.New("fail"), 1)
	if node.HealthState() != BackendHealthUnhealthy {
		t.Fatalf("expected unhealthy")
	}

	recoveryThreshold := 2

	// Success 1: transitions to recovering
	node.RecordHealthSuccess(1, recoveryThreshold)
	if node.HealthState() != BackendHealthRecovering {
		t.Fatalf("expected BackendHealthRecovering after 1 success, got %s", node.HealthState())
	}
	if node.IsAvailable() {
		t.Fatal("recovering node must not be available until fully healthy")
	}

	// Success 2: transitions to healthy
	node.RecordHealthSuccess(1, recoveryThreshold)
	if node.HealthState() != BackendHealthHealthy {
		t.Fatalf("expected BackendHealthHealthy after 2 successes, got %s", node.HealthState())
	}
	if !node.IsAvailable() {
		t.Fatal("healthy node must be available for traffic")
	}
}

func TestBackendHealth_OneUnhealthyDoesNotDisableHealthy(t *testing.T) {
	backendHealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend-Id", "node-healthy")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"healthy":true}`))
	}))
	defer backendHealthy.Close()

	uHealthy, _ := url.Parse(backendHealthy.URL)
	uDown, _ := url.Parse("http://127.0.0.1:59998")

	nodeHealthy := NewBackendNode(uHealthy, nil, http.DefaultTransport)
	nodeDown := NewBackendNode(uDown, nil, http.DefaultTransport)

	// Node down marked unhealthy
	nodeDown.RecordHealthFailure(errors.New("dial error"), 1)

	pool, _ := NewBackendPool([]*BackendNode{nodeHealthy, nodeDown})
	handler := newGatewayHandler(testAPIKey, pool, &mockLimiterAllowed{})

	req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req.Header.Set("X-API-Key", testAPIKey)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from healthy peer, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"healthy":true`) {
		t.Fatalf("expected body from healthy backend, got %s", rr.Body.String())
	}
}

func TestBackendHealth_AllUnhealthyControlled503(t *testing.T) {
	u1, _ := url.Parse("http://127.0.0.1:59997")
	u2, _ := url.Parse("http://127.0.0.1:59998")

	node1 := NewBackendNode(u1, nil, http.DefaultTransport)
	node2 := NewBackendNode(u2, nil, http.DefaultTransport)

	// Both marked unhealthy
	node1.RecordHealthFailure(errors.New("down"), 1)
	node2.RecordHealthFailure(errors.New("down"), 1)

	pool, _ := NewBackendPool([]*BackendNode{node1, node2})
	handler := newGatewayHandler(testAPIKey, pool, &mockLimiterAllowed{})

	req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req.Header.Set("X-API-Key", testAPIKey)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable when all backends unhealthy, got %d", rr.Code)
	}
	if retryAfter := rr.Header().Get("Retry-After"); retryAfter == "" {
		t.Fatal("expected Retry-After header on 503 response")
	}
	if !strings.Contains(rr.Body.String(), "all backends are unhealthy") {
		t.Fatalf("expected body to mention all backends are unhealthy, got %s", rr.Body.String())
	}
}

func TestBackendHealth_ConcurrentHealthChecksRaceFree(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if healthy.Load() {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	node := NewBackendNode(u, nil, http.DefaultTransport)
	pool, _ := NewBackendPool([]*BackendNode{node})

	hc := NewHealthChecker(pool, 10*time.Millisecond, "/health", 200*time.Millisecond, 2, 2)
	hc.Start()
	defer hc.Stop()

	handler := newGatewayHandler(testAPIKey, pool, &mockLimiterAllowed{})

	const concurrency = 20
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if id%2 == 0 {
					healthy.Store(false)
				} else {
					healthy.Store(true)
				}
				req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
				req.Header.Set("X-API-Key", testAPIKey)
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, req)
			}
		}(i)
	}
	wg.Wait()
}

func TestBackendHealth_EndpointReturnsDiagnostics(t *testing.T) {
	u1, _ := url.Parse("http://localhost:8081")
	u2, _ := url.Parse("http://localhost:8082")

	node1 := NewBackendNode(u1, nil, http.DefaultTransport)
	node2 := NewBackendNode(u2, nil, http.DefaultTransport)
	for i := 0; i < 3; i++ {
		node2.RecordHealthFailure(errors.New("connection timeout"), 3)
	}

	pool, _ := NewBackendPool([]*BackendNode{node1, node2})
	handler := newGatewayHandler(testAPIKey, pool, &mockLimiterAllowed{})

	// 1. Unauthenticated -> 401
	reqUnauth := httptest.NewRequest(http.MethodGet, "/api/backends", nil)
	rrUnauth := httptest.NewRecorder()
	handler.ServeHTTP(rrUnauth, reqUnauth)
	if rrUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rrUnauth.Code)
	}

	// 2. Authenticated -> 200 with diagnostics
	reqAuth := httptest.NewRequest(http.MethodGet, "/api/backends", nil)
	reqAuth.Header.Set("X-API-Key", testAPIKey)
	rrAuth := httptest.NewRecorder()
	handler.ServeHTTP(rrAuth, reqAuth)

	if rrAuth.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rrAuth.Code)
	}

	var resp struct {
		Backends []struct {
			ID                  string `json:"id"`
			URL                 string `json:"url"`
			HealthStatus        string `json:"healthStatus"`
			CircuitBreakerState string `json:"circuitBreakerState"`
			ConsecutiveFailures int    `json:"consecutiveFailures"`
			LastError           string `json:"lastError"`
		} `json:"backends"`
	}
	if err := json.Unmarshal(rrAuth.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse /api/backends JSON: %v", err)
	}
	if len(resp.Backends) != 2 {
		t.Fatalf("expected 2 backends in response, got %d", len(resp.Backends))
	}

	b1 := resp.Backends[0]
	if b1.HealthStatus != "healthy" {
		t.Errorf("expected b1 healthy, got %s", b1.HealthStatus)
	}

	b2 := resp.Backends[1]
	if b2.HealthStatus != "unhealthy" {
		t.Errorf("expected b2 unhealthy, got %s", b2.HealthStatus)
	}
	if b2.ConsecutiveFailures != 3 {
		t.Errorf("expected b2 consecutiveFailures 3, got %d", b2.ConsecutiveFailures)
	}
	if !strings.Contains(b2.LastError, "connection timeout") {
		t.Errorf("expected b2 lastError to contain connection timeout, got %q", b2.LastError)
	}
}

func TestBackendPoolReplaceNodesAndMaintenanceExclusion(t *testing.T) {
	firstCalls := atomic.Int64{}
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer first.Close()
	secondCalls := atomic.Int64{}
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()
	firstURL, _ := url.Parse(first.URL)
	secondURL, _ := url.Parse(second.URL)
	firstNode := NewBackendNode(firstURL, nil, http.DefaultTransport)
	secondNode := NewBackendNode(secondURL, nil, http.DefaultTransport)
	pool, err := NewBackendPool([]*BackendNode{firstNode})
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.ReplaceNodes([]*BackendNode{firstNode, secondNode}); err != nil {
		t.Fatalf("replace backend membership: %v", err)
	}
	if len(pool.Nodes()) != 2 {
		t.Fatalf("pool node count=%d, want 2", len(pool.Nodes()))
	}
	firstNode.maintenance.Store(true)
	if got := pool.SelectNode(); got != secondNode {
		t.Fatalf("maintenance backend selected: got %s, want %s", got.Name(), secondNode.Name())
	}
	pool.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if firstCalls.Load() != 0 || secondCalls.Load() != 1 {
		t.Fatalf("maintenance exclusion calls: first=%d second=%d", firstCalls.Load(), secondCalls.Load())
	}
	if err := pool.ReplaceNodes([]*BackendNode{secondNode}); err != nil {
		t.Fatalf("remove first backend: %v", err)
	}
	if len(pool.Nodes()) != 1 || pool.Nodes()[0] != secondNode {
		t.Fatalf("pool did not atomically publish replacement nodes: %+v", pool.Nodes())
	}
	if err := pool.ReplaceNodes(nil); err == nil {
		t.Fatal("empty backend replacement should fail")
	}
}
