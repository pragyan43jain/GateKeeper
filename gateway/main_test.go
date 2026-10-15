package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

const testAPIKey = "valid-secret-key-12345"

func setupTestRedis(t *testing.T) (*redis.Client, string) {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping Redis test: Redis not reachable on localhost:6379: %v", err)
	}

	testPrefix := fmt.Sprintf("gatekeeper:test:%d:", time.Now().UnixNano())

	t.Cleanup(func() {
		// Clean up all keys with the test prefix
		ctx := context.Background()
		keys, err := rdb.Keys(ctx, testPrefix+"*").Result()
		if err == nil && len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
		rdb.Close()
	})

	return rdb, testPrefix
}

func setupTestGateway(t *testing.T, limiter RateLimiter) (http.Handler, *httptest.Server) {
	t.Helper()

	// Backend server that returns 200 OK with a test response
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","backend":true}`))
	}))
	t.Cleanup(backend.Close)

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("failed to parse test backend url: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(backendURL)
	handler := newGatewayHandler(testAPIKey, proxy, limiter)
	return handler, backend
}

func setupTestGatewayWithMetrics(t *testing.T, limiter RateLimiter, metrics *GatewayMetrics) (http.Handler, *httptest.Server) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","backend":true}`))
	}))
	t.Cleanup(backend.Close)

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("failed to parse test backend url: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(backendURL)
	handler := newGatewayHandler(testAPIKey, proxy, limiter, metrics)
	return handler, backend
}

// mockLimiterAllowed simulates a rate limiter that always permits requests.
type mockLimiterAllowed struct{}

func (m *mockLimiterAllowed) Allow(ctx context.Context, identity string) (RateLimitResult, error) {
	return RateLimitResult{
		Allowed:    true,
		Limit:      10,
		Remaining:  9,
		RetryAfter: 0,
	}, nil
}

// TestPublicHealthEndpoint verifies /health is public and does not require authentication or rate limits.
func TestPublicHealthEndpoint(t *testing.T) {
	rdb, prefix := setupTestRedis(t)
	limiter := NewRedisRateLimiter(rdb, prefix, 10, 60*time.Second)
	handler, _ := setupTestGateway(t, limiter)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	if body := rr.Body.String(); body != "Gatekeeper is healthy\n" {
		t.Fatalf("unexpected body: %q", body)
	}
}

// TestAuthentication_MissingAndInvalidKeys verifies unauthorized requests are rejected before hitting Redis.
func TestAuthentication_MissingAndInvalidKeys(t *testing.T) {
	rdb, prefix := setupTestRedis(t)
	limiter := NewRedisRateLimiter(rdb, prefix, 10, 60*time.Second)
	handler, _ := setupTestGateway(t, limiter)

	tests := []struct {
		name       string
		keyHeader  string
		wantStatus int
	}{
		{
			name:       "Missing API Key",
			keyHeader:  "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "Invalid API Key",
			keyHeader:  "incorrect-key",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
			if tc.keyHeader != "" {
				req.Header.Set("X-API-Key", tc.keyHeader)
			}
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d", tc.wantStatus, rr.Code)
			}

			// Ensure Redis was not touched (no keys created)
			keys, err := rdb.Keys(context.Background(), prefix+"*").Result()
			if err != nil {
				t.Fatalf("failed to query redis keys: %v", err)
			}
			if len(keys) != 0 {
				t.Fatalf("expected 0 Redis keys for unauthenticated requests, found %d", len(keys))
			}
		})
	}
}

// TestRateLimiter_WithinLimit verifies requests within the limit succeed and are proxied.
func TestRateLimiter_WithinLimit(t *testing.T) {
	rdb, prefix := setupTestRedis(t)
	limiter := NewRedisRateLimiter(rdb, prefix, 10, 60*time.Second)
	handler, _ := setupTestGateway(t, limiter)

	for i := 1; i <= 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200 OK, got %d", i, rr.Code)
		}

		if gatewayHeader := rr.Header().Get("X-Gateway"); gatewayHeader != "Gatekeeper" {
			t.Fatalf("request %d: expected X-Gateway header 'Gatekeeper', got %q", i, gatewayHeader)
		}

		remainingHeader := rr.Header().Get("X-RateLimit-Remaining")
		expectedRemaining := strconv.Itoa(10 - i)
		if remainingHeader != expectedRemaining {
			t.Fatalf("request %d: expected X-RateLimit-Remaining %s, got %s", i, expectedRemaining, remainingHeader)
		}
	}
}

// TestRateLimiter_ExceedingLimit verifies requests exceeding 10 are blocked with 429 and Retry-After header.
func TestRateLimiter_ExceedingLimit(t *testing.T) {
	rdb, prefix := setupTestRedis(t)
	limiter := NewRedisRateLimiter(rdb, prefix, 10, 60*time.Second)
	handler, _ := setupTestGateway(t, limiter)

	// Send 10 allowed requests
	for i := 1; i <= 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200 OK, got %d", i, rr.Code)
		}
	}

	// 11th request must be rate limited
	req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req.Header.Set("X-API-Key", testAPIKey)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429 Too Many Requests, got %d", rr.Code)
	}

	retryAfter := rr.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("expected Retry-After header to be present on 429 response")
	}

	retrySecs, err := strconv.Atoi(retryAfter)
	if err != nil || retrySecs <= 0 || retrySecs > 60 {
		t.Fatalf("expected Retry-After to be between 1 and 60 seconds, got %q (err: %v)", retryAfter, err)
	}

	if body := rr.Body.String(); body == "" {
		t.Fatal("expected non-empty error message on rate limit response")
	}
}

// TestRateLimiter_Concurrency verifies concurrent requests cannot bypass the limit.
func TestRateLimiter_Concurrency(t *testing.T) {
	rdb, prefix := setupTestRedis(t)
	limiter := NewRedisRateLimiter(rdb, prefix, 10, 60*time.Second)
	handler, _ := setupTestGateway(t, limiter)

	totalRequests := 25
	var wg sync.WaitGroup
	statusCodes := make([]int, totalRequests)

	for i := 0; i < totalRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
			req.Header.Set("X-API-Key", testAPIKey)
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)
			statusCodes[idx] = rr.Code
		}(i)
	}

	wg.Wait()

	allowedCount := 0
	rateLimitedCount := 0
	for _, code := range statusCodes {
		if code == http.StatusOK {
			allowedCount++
		} else if code == http.StatusTooManyRequests {
			rateLimitedCount++
		} else {
			t.Fatalf("unexpected status code during concurrency test: %d", code)
		}
	}

	if allowedCount != 10 {
		t.Fatalf("expected exactly 10 requests allowed, got %d", allowedCount)
	}
	if rateLimitedCount != (totalRequests - 10) {
		t.Fatalf("expected %d requests rate limited, got %d", totalRequests-10, rateLimitedCount)
	}
}

// mockFailingLimiter simulates Redis outage.
type mockFailingLimiter struct{}

func (m *mockFailingLimiter) Allow(ctx context.Context, identity string) (RateLimitResult, error) {
	return RateLimitResult{}, errors.New("dial tcp 127.0.0.1:6379: connect: connection refused")
}

// TestRateLimiter_RedisUnavailable verifies that when Redis fails, API requests fail safely with 503
// without bypassing rate limiting, and /health remains operational.
func TestRateLimiter_RedisUnavailable(t *testing.T) {
	limiter := &mockFailingLimiter{}
	handler, _ := setupTestGateway(t, limiter)

	// API request must fail with 503 Service Unavailable
	req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req.Header.Set("X-API-Key", testAPIKey)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503 Service Unavailable when Redis is down, got %d", rr.Code)
	}

	// /health must remain functional even when Redis is down
	healthReq := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthRR := httptest.NewRecorder()

	handler.ServeHTTP(healthRR, healthReq)

	if healthRR.Code != http.StatusOK {
		t.Fatalf("expected /health to return 200 OK even when Redis is down, got %d", healthRR.Code)
	}
}

// TestRateLimiter_UnreachableRedisClient tests using real Redis client pointed at unreachable port.
func TestRateLimiter_UnreachableRedisClient(t *testing.T) {
	badClient := redis.NewClient(&redis.Options{
		Addr:        "localhost:59999", // Unused port
		DialTimeout: 200 * time.Millisecond,
	})
	defer badClient.Close()

	limiter := NewRedisRateLimiter(badClient, "gatekeeper:test:bad:", 10, 60*time.Second)
	handler, _ := setupTestGateway(t, limiter)

	req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req.Header.Set("X-API-Key", testAPIKey)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503 Service Unavailable, got %d", rr.Code)
	}
}

// TestHashAPIKey_StabilityAndPrivacy verifies that the hash is stable, deterministic, and does not leak the key.
func TestHashAPIKey_StabilityAndPrivacy(t *testing.T) {
	key := "my-secret-key-123"
	hash1 := hashAPIKey(key)
	hash2 := hashAPIKey(key)

	if hash1 != hash2 {
		t.Fatalf("expected hash to be deterministic: %q != %q", hash1, hash2)
	}

	if len(hash1) != 64 {
		t.Fatalf("expected 64-character SHA-256 hex string, got len %d (%q)", len(hash1), hash1)
	}

	// Different keys must produce different hashes
	otherHash := hashAPIKey("another-secret-key-456")
	if hash1 == otherHash {
		t.Fatal("different keys produced identical hash")
	}
}

// TestMetrics_EndpointAccessibleWithoutAuth verifies that /metrics returns 200 OK without requiring an API key.
func TestMetrics_EndpointAccessibleWithoutAuth(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewGatewayMetrics(reg)
	mockLimiter := &mockLimiterAllowed{}
	handler, _ := setupTestGatewayWithMetrics(t, mockLimiter, metrics)

	// Send an initial request so vector metrics have observed samples
	reqInit := httptest.NewRequest(http.MethodGet, "/health", nil)
	rrInit := httptest.NewRecorder()
	handler.ServeHTTP(rrInit, reqInit)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200 OK from /metrics, got %d", rr.Code)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "gateway_requests_total") {
		t.Errorf("expected /metrics output to contain 'gateway_requests_total', got:\n%s", body)
	}
	if !strings.Contains(body, "gateway_request_duration_seconds") {
		t.Errorf("expected /metrics output to contain 'gateway_request_duration_seconds', got:\n%s", body)
	}
	if !strings.Contains(body, "gateway_rate_limit_rejections_total") {
		t.Errorf("expected /metrics output to contain 'gateway_rate_limit_rejections_total', got:\n%s", body)
	}
}

// TestMetrics_ExcludesMetricsScrapes verifies that /metrics scrapes are not counted in application request metrics.
func TestMetrics_ExcludesMetricsScrapes(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewGatewayMetrics(reg)
	mockLimiter := &mockLimiterAllowed{}
	handler, _ := setupTestGatewayWithMetrics(t, mockLimiter, metrics)

	// Scrape /metrics multiple times
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected /metrics to return 200 OK, got %d", rr.Code)
		}
	}

	// Fetch /metrics to inspect output
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	// gateway_requests_total should have HELP and TYPE headers, but NO sample entries with labels
	if strings.Contains(body, "gateway_requests_total{") {
		t.Errorf("expected no gateway_requests_total sample entries for /metrics scrapes, got:\n%s", body)
	}
}

// TestMetrics_RequestCountingAndLatency verifies that status codes and durations are tracked accurately.
func TestMetrics_RequestCountingAndLatency(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewGatewayMetrics(reg)
	mockLimiter := &mockLimiterAllowed{}
	handler, _ := setupTestGatewayWithMetrics(t, mockLimiter, metrics)

	// 1. One authenticated 200 OK request to /api/students
	req200 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req200.Header.Set("X-API-Key", testAPIKey)
	rr200 := httptest.NewRecorder()
	handler.ServeHTTP(rr200, req200)
	if rr200.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr200.Code)
	}

	// 2. One unauthenticated 401 Unauthorized request to /api/students
	req401 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	rr401 := httptest.NewRecorder()
	handler.ServeHTTP(rr401, req401)
	if rr401.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr401.Code)
	}

	// 3. One request to /health
	reqHealth := httptest.NewRequest(http.MethodGet, "/health", nil)
	rrHealth := httptest.NewRecorder()
	handler.ServeHTTP(rrHealth, reqHealth)
	if rrHealth.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rrHealth.Code)
	}

	// Fetch /metrics
	reqM := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rrM := httptest.NewRecorder()
	handler.ServeHTTP(rrM, reqM)

	body := rrM.Body.String()

	// 2 successful 200 requests (one /api/students, one /health)
	if !strings.Contains(body, `gateway_requests_total{code="200",method="GET"} 2`) {
		t.Errorf("expected gateway_requests_total{code=\"200\",method=\"GET\"} 2, got:\n%s", body)
	}
	// 1 unauthorized 401 request
	if !strings.Contains(body, `gateway_requests_total{code="401",method="GET"} 1`) {
		t.Errorf("expected gateway_requests_total{code=\"401\",method=\"GET\"} 1, got:\n%s", body)
	}
	// Latency histogram samples
	if !strings.Contains(body, `gateway_request_duration_seconds_count{code="200",method="GET"} 2`) {
		t.Errorf("expected duration count 2 for code 200, got:\n%s", body)
	}
	if !strings.Contains(body, `gateway_request_duration_seconds_count{code="401",method="GET"} 1`) {
		t.Errorf("expected duration count 1 for code 401, got:\n%s", body)
	}
}

// TestMetrics_RateLimitRejectionCount verifies that rate limit rejections (429) are tracked.
func TestMetrics_RateLimitRejectionCount(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewGatewayMetrics(reg)

	rdb, prefix := setupTestRedis(t)
	limiter := NewRedisRateLimiter(rdb, prefix, 2, 60*time.Second) // limit 2 requests
	handler, _ := setupTestGatewayWithMetrics(t, limiter, metrics)

	// Send 2 allowed requests
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rr.Code)
		}
	}

	// Send 2 requests exceeding limit (429)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusTooManyRequests {
			t.Fatalf("expected 429, got %d", rr.Code)
		}
	}

	// Fetch /metrics
	reqM := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rrM := httptest.NewRecorder()
	handler.ServeHTTP(rrM, reqM)

	body := rrM.Body.String()
	if !strings.Contains(body, `gateway_rate_limit_rejections_total 2`) {
		t.Errorf("expected gateway_rate_limit_rejections_total 2, got:\n%s", body)
	}
	if !strings.Contains(body, `gateway_requests_total{code="429",method="GET"} 2`) {
		t.Errorf("expected gateway_requests_total{code=\"429\",method=\"GET\"} 2, got:\n%s", body)
	}
}

// TestMetrics_ReverseProxyErrorCaptured verifies that reverse proxy 502 errors are recorded in metrics.
func TestMetrics_ReverseProxyErrorCaptured(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewGatewayMetrics(reg)
	mockLimiter := &mockLimiterAllowed{}

	// Unreachable target
	unreachableURL, _ := url.Parse("http://127.0.0.1:59998")
	proxy := httputil.NewSingleHostReverseProxy(unreachableURL)
	handler := newGatewayHandler(testAPIKey, proxy, mockLimiter, metrics)

	req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req.Header.Set("X-API-Key", testAPIKey)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway from unreachable proxy, got %d", rr.Code)
	}

	reqM := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rrM := httptest.NewRecorder()
	handler.ServeHTTP(rrM, reqM)

	body := rrM.Body.String()
	if !strings.Contains(body, `gateway_requests_total{code="502",method="GET"} 1`) {
		t.Errorf("expected gateway_requests_total{code=\"502\",method=\"GET\"} 1, got:\n%s", body)
	}
}

// mockFlusherWriter verifies http.Flusher delegation.
type mockFlusherWriter struct {
	http.ResponseWriter
	flushed bool
}

func (m *mockFlusherWriter) Flush() {
	m.flushed = true
}

// TestMetrics_StatusResponseWriterFlusherAndUnwrap verifies http.Flusher and Unwrap support.
func TestMetrics_StatusResponseWriterFlusherAndUnwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	mock := &mockFlusherWriter{ResponseWriter: rec}
	srw := newStatusResponseWriter(mock)

	srw.Flush()
	if !mock.flushed {
		t.Fatal("expected Flush() to be delegated to underlying flusher")
	}

	if srw.Unwrap() != mock {
		t.Fatal("expected Unwrap() to return underlying ResponseWriter")
	}
}

// fakeRoundTripper enables deterministic testing of upstream transport behavior.
type fakeRoundTripper struct {
	mu         sync.Mutex
	calls      int
	statusCode int
	body       string
	err        error
	onCall     func(req *http.Request) (*http.Response, error)
}

func (f *fakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.calls++
	onCall := f.onCall
	statusCode := f.statusCode
	body := f.body
	err := f.err
	f.mu.Unlock()

	if onCall != nil {
		return onCall(req)
	}

	if err != nil {
		return nil, err
	}

	if statusCode == 0 {
		statusCode = http.StatusOK
	}

	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	return &http.Response{
		StatusCode:    statusCode,
		Status:        fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
		Header:        header,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}, nil
}

func (f *fakeRoundTripper) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// TestCircuitBreaker_ClosedPermitsRequests verifies requests succeed through a closed circuit.
func TestCircuitBreaker_ClosedPermitsRequests(t *testing.T) {
	cb := NewCircuitBreaker(3, 10*time.Second)
	fake := &fakeRoundTripper{statusCode: http.StatusOK, body: `{"status":"ok"}`}

	targetURL, _ := url.Parse("http://backend.internal")
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = NewCircuitBreakerTransport(cb, fake)

	req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	rr := httptest.NewRecorder()

	proxy.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rr.Code)
	}
	if fake.Calls() != 1 {
		t.Fatalf("expected 1 call to transport, got %d", fake.Calls())
	}
	if cb.State() != StateClosed {
		t.Fatalf("expected circuit to be closed, got %s", cb.State())
	}
	if cb.ConsecutiveFailures() != 0 {
		t.Fatalf("expected 0 failures, got %d", cb.ConsecutiveFailures())
	}
}

// TestCircuitBreaker_RepeatedErrorsOpenAndThresholdReceives502 verifies that consecutive failures
// open the circuit and that the threshold-triggering request receives 502 Bad Gateway.
func TestCircuitBreaker_RepeatedErrorsOpenAndThresholdReceives502(t *testing.T) {
	threshold := 3
	cb := NewCircuitBreaker(threshold, 10*time.Second)
	fake := &fakeRoundTripper{err: errors.New("dial tcp: connection refused")}

	targetURL, _ := url.Parse("http://backend.internal")
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = NewCircuitBreakerTransport(cb, fake)

	for i := 1; i <= threshold; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		rr := httptest.NewRecorder()

		proxy.ServeHTTP(rr, req)

		// Each transport failure returns 502 Bad Gateway
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("request %d: expected 502 Bad Gateway, got %d", i, rr.Code)
		}

		if cb.ConsecutiveFailures() != i {
			t.Fatalf("request %d: expected %d consecutive failures, got %d", i, i, cb.ConsecutiveFailures())
		}

		if i < threshold {
			if cb.State() != StateClosed {
				t.Fatalf("request %d: expected circuit to still be closed, got %s", i, cb.State())
			}
		} else {
			// The threshold-triggering request opens the circuit
			if cb.State() != StateOpen {
				t.Fatalf("threshold request %d: expected circuit to transition to Open, got %s", i, cb.State())
			}
		}
	}

	if fake.Calls() != threshold {
		t.Fatalf("expected exactly %d transport attempts, got %d", threshold, fake.Calls())
	}
}

// TestCircuitBreaker_OpenReturns503WithoutCallingTransport verifies that requests while open receive
// 503 Service Unavailable and a Retry-After header without calling the backend transport.
func TestCircuitBreaker_OpenReturns503WithoutCallingTransport(t *testing.T) {
	threshold := 2
	cooldown := 10 * time.Second
	clock := newFakeClock(time.Now())
	cb := NewCircuitBreaker(threshold, cooldown, clock.Now)
	fake := &fakeRoundTripper{err: errors.New("dial tcp: connection refused")}

	targetURL, _ := url.Parse("http://backend.internal")
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = NewCircuitBreakerTransport(cb, fake)

	// Trip the circuit breaker
	for i := 0; i < threshold; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		rr := httptest.NewRecorder()
		proxy.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("expected 502, got %d", rr.Code)
		}
	}

	if cb.State() != StateOpen {
		t.Fatalf("expected circuit to be open, got %s", cb.State())
	}
	initialCalls := fake.Calls()

	// Advance time partially (4 seconds into 10s cooldown -> 6 seconds remaining)
	clock.Advance(4 * time.Second)

	// Request while open must return 503 with Retry-After header
	req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable, got %d", rr.Code)
	}

	retryAfter := rr.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("expected Retry-After header to be present on 503 response")
	}
	retrySecs, err := strconv.Atoi(retryAfter)
	if err != nil || retrySecs != 6 {
		t.Fatalf("expected Retry-After to be 6 seconds, got %q (err: %v)", retryAfter, err)
	}

	if !strings.Contains(rr.Body.String(), "circuit breaker is open") {
		t.Fatalf("expected body to contain 'circuit breaker is open', got: %q", rr.Body.String())
	}

	// Verify the backend transport was NOT called
	if fake.Calls() != initialCalls {
		t.Fatalf("expected backend transport not to be called while open; calls went from %d to %d", initialCalls, fake.Calls())
	}
}

// TestCircuitBreaker_HalfOpenAllowsSingleProbeUnderConcurrency verifies that when half-open,
// exactly one probe request runs while concurrent requests fail fast with 503.
func TestCircuitBreaker_HalfOpenAllowsSingleProbeUnderConcurrency(t *testing.T) {
	threshold := 2
	cooldown := 10 * time.Second
	clock := newFakeClock(time.Now())
	cb := NewCircuitBreaker(threshold, cooldown, clock.Now)

	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})

	fake := &fakeRoundTripper{
		onCall: func(req *http.Request) (*http.Response, error) {
			select {
			case <-probeStarted:
			default:
				close(probeStarted)
			}
			<-releaseProbe
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader(`{"probe":"ok"}`)),
				ContentLength: 14,
				Request:       req,
			}, nil
		},
	}

	targetURL, _ := url.Parse("http://backend.internal")
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = NewCircuitBreakerTransport(cb, fake)

	// Trip the circuit breaker
	cb.afterRequest(false, errors.New("trip-1"))
	cb.afterRequest(false, errors.New("trip-2"))
	if cb.State() != StateOpen {
		t.Fatalf("expected StateOpen, got %s", cb.State())
	}

	// Advance clock past cooldown
	clock.Advance(cooldown + 1*time.Second)

	// Launch probe request in background goroutine
	probeCodeCh := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		rr := httptest.NewRecorder()
		proxy.ServeHTTP(rr, req)
		probeCodeCh <- rr.Code
	}()

	// Wait until the probe has actually reached fake transport and is blocked
	select {
	case <-probeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for probe to start")
	}

	// While the probe is in-flight, send 10 concurrent requests
	const concurrentCount = 10
	var wg sync.WaitGroup
	fastFailCodes := make([]int, concurrentCount)

	for i := 0; i < concurrentCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
			rr := httptest.NewRecorder()
			proxy.ServeHTTP(rr, req)
			fastFailCodes[idx] = rr.Code
		}(i)
	}

	wg.Wait()

	// All concurrent requests must have failed fast with 503
	for i, code := range fastFailCodes {
		if code != http.StatusServiceUnavailable {
			t.Fatalf("concurrent request %d: expected 503, got %d", i, code)
		}
	}

	// Now release the blocked probe
	close(releaseProbe)

	probeCode := <-probeCodeCh
	if probeCode != http.StatusOK {
		t.Fatalf("expected probe request to succeed with 200 OK, got %d", probeCode)
	}

	// Exactly 1 request reached the fake transport
	if fake.Calls() != 1 {
		t.Fatalf("expected exactly 1 call to fake transport during probe, got %d", fake.Calls())
	}

	// Circuit breaker should now be closed because the probe succeeded
	if cb.State() != StateClosed {
		t.Fatalf("expected circuit to transition to Closed after successful probe, got %s", cb.State())
	}
}

// TestCircuitBreaker_SuccessfulProbeClosesCircuit verifies a successful probe closes the circuit.
func TestCircuitBreaker_SuccessfulProbeClosesCircuit(t *testing.T) {
	threshold := 2
	cooldown := 10 * time.Second
	clock := newFakeClock(time.Now())
	cb := NewCircuitBreaker(threshold, cooldown, clock.Now)

	var shouldFail bool
	fake := &fakeRoundTripper{
		onCall: func(req *http.Request) (*http.Response, error) {
			if shouldFail {
				return nil, errors.New("upstream unreachable")
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader("ok")),
				ContentLength: 2,
				Request:       req,
			}, nil
		},
	}

	targetURL, _ := url.Parse("http://backend.internal")
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = NewCircuitBreakerTransport(cb, fake)

	// Trip to open
	shouldFail = true
	for i := 0; i < threshold; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		rr := httptest.NewRecorder()
		proxy.ServeHTTP(rr, req)
	}
	if cb.State() != StateOpen {
		t.Fatalf("expected StateOpen, got %s", cb.State())
	}

	// Cooldown expires, upstream recovers
	clock.Advance(cooldown)
	shouldFail = false

	// Trial probe request
	req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected probe to return 200, got %d", rr.Code)
	}

	if cb.State() != StateClosed {
		t.Fatalf("expected circuit to be Closed, got %s", cb.State())
	}
	if cb.ConsecutiveFailures() != 0 {
		t.Fatalf("expected 0 failures, got %d", cb.ConsecutiveFailures())
	}

	// Subsequent requests continue to succeed
	req2 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	rr2 := httptest.NewRecorder()
	proxy.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected subsequent request to return 200, got %d", rr2.Code)
	}
}

// TestCircuitBreaker_FailedProbeReopensCircuit verifies that a failed probe reopens the circuit.
func TestCircuitBreaker_FailedProbeReopensCircuit(t *testing.T) {
	threshold := 2
	cooldown := 10 * time.Second
	clock := newFakeClock(time.Now())
	cb := NewCircuitBreaker(threshold, cooldown, clock.Now)
	fake := &fakeRoundTripper{err: errors.New("upstream still down")}

	targetURL, _ := url.Parse("http://backend.internal")
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = NewCircuitBreakerTransport(cb, fake)

	// Trip breaker
	for i := 0; i < threshold; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		rr := httptest.NewRecorder()
		proxy.ServeHTTP(rr, req)
	}
	if cb.State() != StateOpen {
		t.Fatalf("expected StateOpen, got %s", cb.State())
	}

	// Cooldown elapses
	clock.Advance(cooldown)

	// Probe request fails
	probeReq := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	probeRR := httptest.NewRecorder()
	proxy.ServeHTTP(probeRR, probeReq)

	if probeRR.Code != http.StatusBadGateway {
		t.Fatalf("expected probe failure to return 502, got %d", probeRR.Code)
	}

	// Circuit must be Open again
	if cb.State() != StateOpen {
		t.Fatalf("expected circuit to reopen to StateOpen, got %s", cb.State())
	}

	callsAfterProbe := fake.Calls()

	// Immediate next request must fail fast with 503 without calling transport
	nextReq := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	nextRR := httptest.NewRecorder()
	proxy.ServeHTTP(nextRR, nextReq)

	if nextRR.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 after reopened probe, got %d", nextRR.Code)
	}
	if fake.Calls() != callsAfterProbe {
		t.Fatalf("expected no additional transport calls while reopened")
	}
}

// TestCircuitBreaker_Backend500DoesNotTripBreaker verifies that backend HTTP 500 responses
// do not count as transport failures.
func TestCircuitBreaker_Backend500DoesNotTripBreaker(t *testing.T) {
	cb := NewCircuitBreaker(2, 10*time.Second)
	fake := &fakeRoundTripper{
		statusCode: http.StatusInternalServerError,
		body:       `{"error":"database query failed"}`,
	}

	targetURL, _ := url.Parse("http://backend.internal")
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = NewCircuitBreakerTransport(cb, fake)

	for i := 1; i <= 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		rr := httptest.NewRecorder()
		proxy.ServeHTTP(rr, req)

		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("request %d: expected 500, got %d", i, rr.Code)
		}
	}

	if cb.State() != StateClosed {
		t.Fatalf("expected circuit to remain Closed on 500 responses, got %s", cb.State())
	}
	if cb.ConsecutiveFailures() != 0 {
		t.Fatalf("expected consecutive failures to remain 0 on 500 responses, got %d", cb.ConsecutiveFailures())
	}
	if fake.Calls() != 5 {
		t.Fatalf("expected 5 calls to backend, got %d", fake.Calls())
	}
}

// TestCircuitBreaker_AuthAndRateLimitDoNotAffectBreaker verifies that gateway 401 and 429
// responses do not affect circuit breaker counters or state.
func TestCircuitBreaker_AuthAndRateLimitDoNotAffectBreaker(t *testing.T) {
	cb := NewCircuitBreaker(2, 10*time.Second)
	fake := &fakeRoundTripper{statusCode: http.StatusOK, body: `{"status":"ok"}`}

	targetURL, _ := url.Parse("http://backend.internal")
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = NewCircuitBreakerTransport(cb, fake)

	rdb, prefix := setupTestRedis(t)
	limiter := NewRedisRateLimiter(rdb, prefix, 2, 60*time.Second)
	handler := newGatewayHandler(testAPIKey, proxy, limiter)

	// 1. Missing API key -> 401
	reqMissing := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	rrMissing := httptest.NewRecorder()
	handler.ServeHTTP(rrMissing, reqMissing)
	if rrMissing.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rrMissing.Code)
	}

	// 2. Invalid API key -> 401
	reqInvalid := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	reqInvalid.Header.Set("X-API-Key", "wrong-key")
	rrInvalid := httptest.NewRecorder()
	handler.ServeHTTP(rrInvalid, reqInvalid)
	if rrInvalid.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rrInvalid.Code)
	}

	// Assert transport was not called and breaker state is untouched
	if fake.Calls() != 0 {
		t.Fatalf("expected 0 calls to transport for 401s, got %d", fake.Calls())
	}
	if cb.State() != StateClosed || cb.ConsecutiveFailures() != 0 {
		t.Fatalf("expected breaker to remain closed with 0 failures after 401s")
	}

	// 3. Send 2 valid authenticated requests within limit
	for i := 1; i <= 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200 OK, got %d", i, rr.Code)
		}
	}
	if fake.Calls() != 2 {
		t.Fatalf("expected 2 calls to transport, got %d", fake.Calls())
	}

	// 4. Send 3rd request -> 429 Too Many Requests
	reqExceeded := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	reqExceeded.Header.Set("X-API-Key", testAPIKey)
	rrExceeded := httptest.NewRecorder()
	handler.ServeHTTP(rrExceeded, reqExceeded)
	if rrExceeded.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rrExceeded.Code)
	}

	// Transport must still be at 2 calls; breaker untouched
	if fake.Calls() != 2 {
		t.Fatalf("expected 2 calls to transport (rate limit must not call backend), got %d", fake.Calls())
	}
	if cb.State() != StateClosed || cb.ConsecutiveFailures() != 0 {
		t.Fatalf("expected breaker to remain closed with 0 failures after 429")
	}
}

// TestCircuitBreaker_RaceSafety verifies that concurrent operations on CircuitBreaker
// and CircuitBreakerTransport are thread-safe and race-free.
func TestCircuitBreaker_RaceSafety(t *testing.T) {
	clock := newFakeClock(time.Now())
	cb := NewCircuitBreaker(3, 50*time.Millisecond, clock.Now)

	var counter int64
	fake := &fakeRoundTripper{
		onCall: func(req *http.Request) (*http.Response, error) {
			c := atomic.AddInt64(&counter, 1)
			if c%3 == 0 {
				return nil, errors.New("simulated network failure")
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader("ok")),
				ContentLength: 2,
				Request:       req,
			}, nil
		},
	}

	targetURL, _ := url.Parse("http://backend.internal")
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = NewCircuitBreakerTransport(cb, fake)

	const workers = 50
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if workerID%5 == 0 {
					clock.Advance(10 * time.Millisecond)
				}
				req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
				rr := httptest.NewRecorder()
				proxy.ServeHTTP(rr, req)

				_ = cb.State()
				_ = cb.ConsecutiveFailures()
			}
		}(i)
	}

	wg.Wait()
}

// TestCircuitBreaker_ConfigurationValidation tests configuration parsing and validation.
func TestCircuitBreaker_ConfigurationValidation(t *testing.T) {
	// 1. Defaults when unset
	os.Unsetenv("TEST_CB_THRESHOLD")
	os.Unsetenv("TEST_CB_COOLDOWN")

	th, err := parseEnvIntPositive("TEST_CB_THRESHOLD", 5)
	if err != nil || th != 5 {
		t.Fatalf("expected default 5, got %d (err: %v)", th, err)
	}

	cd, err := parseEnvDurationPositive("TEST_CB_COOLDOWN", 10*time.Second)
	if err != nil || cd != 10*time.Second {
		t.Fatalf("expected default 10s, got %v (err: %v)", cd, err)
	}

	// 2. Valid positive values
	os.Setenv("TEST_CB_THRESHOLD", "10")
	os.Setenv("TEST_CB_COOLDOWN", "30s")
	defer func() {
		os.Unsetenv("TEST_CB_THRESHOLD")
		os.Unsetenv("TEST_CB_COOLDOWN")
	}()

	th, err = parseEnvIntPositive("TEST_CB_THRESHOLD", 5)
	if err != nil || th != 10 {
		t.Fatalf("expected 10, got %d (err: %v)", th, err)
	}

	cd, err = parseEnvDurationPositive("TEST_CB_COOLDOWN", 10*time.Second)
	if err != nil || cd != 30*time.Second {
		t.Fatalf("expected 30s, got %v (err: %v)", cd, err)
	}

	// 3. Invalid formats
	os.Setenv("TEST_CB_THRESHOLD", "invalid")
	if _, err := parseEnvIntPositive("TEST_CB_THRESHOLD", 5); err == nil {
		t.Fatal("expected error on non-numeric threshold")
	}

	os.Setenv("TEST_CB_COOLDOWN", "not-a-duration")
	if _, err := parseEnvDurationPositive("TEST_CB_COOLDOWN", 10*time.Second); err == nil {
		t.Fatal("expected error on invalid duration")
	}

	// 4. Zero and negative values
	os.Setenv("TEST_CB_THRESHOLD", "0")
	if _, err := parseEnvIntPositive("TEST_CB_THRESHOLD", 5); err == nil {
		t.Fatal("expected error on 0 threshold")
	}

	os.Setenv("TEST_CB_THRESHOLD", "-2")
	if _, err := parseEnvIntPositive("TEST_CB_THRESHOLD", 5); err == nil {
		t.Fatal("expected error on negative threshold")
	}

	os.Setenv("TEST_CB_COOLDOWN", "0s")
	if _, err := parseEnvDurationPositive("TEST_CB_COOLDOWN", 10*time.Second); err == nil {
		t.Fatal("expected error on 0s cooldown")
	}

	os.Setenv("TEST_CB_COOLDOWN", "-5s")
	if _, err := parseEnvDurationPositive("TEST_CB_COOLDOWN", 10*time.Second); err == nil {
		t.Fatal("expected error on negative cooldown")
	}
}
