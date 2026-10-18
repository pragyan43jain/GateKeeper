package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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
	if !strings.Contains(body, "gateway_circuit_breaker_state") {
		t.Errorf("expected /metrics output to contain 'gateway_circuit_breaker_state', got:\n%s", body)
	}
	if !strings.Contains(body, "gateway_circuit_breaker_trips_total") {
		t.Errorf("expected /metrics output to contain 'gateway_circuit_breaker_trips_total', got:\n%s", body)
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

	metrics := NewGatewayMetrics(prometheus.NewRegistry())
	cb.SetMetrics(metrics)

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

// TestMetrics_CircuitBreakerStateAndTrips verifies that circuit breaker state transitions
// correctly update gateway_circuit_breaker_state and gateway_circuit_breaker_trips_total.
func TestMetrics_CircuitBreakerStateAndTrips(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewGatewayMetrics(reg)

	clock := newFakeClock(time.Now())
	cb := NewCircuitBreaker(2, 10*time.Second, clock.Now)
	cb.SetMetrics(metrics)

	var shouldFail bool
	fake := &fakeRoundTripper{
		onCall: func(req *http.Request) (*http.Response, error) {
			if shouldFail {
				return nil, errors.New("upstream failure")
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

	mockLimiter := &mockLimiterAllowed{}
	handler := newGatewayHandler(testAPIKey, proxy, mockLimiter, metrics)

	scrape := func() string {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected /metrics to return 200 OK, got %d", rr.Code)
		}
		return rr.Body.String()
	}

	assertMetricContains := func(desc, expected string) {
		t.Helper()
		body := scrape()
		if !strings.Contains(body, expected) {
			t.Fatalf("[%s] expected /metrics output to contain %q, got:\n%s", desc, expected, body)
		}
	}

	// 1. Initial closed state: state=0, trips=0
	assertMetricContains("Initial closed state", "gateway_circuit_breaker_state 0")
	assertMetricContains("Initial trips count", "gateway_circuit_breaker_trips_total 0")

	// 2. 1 failure (below threshold of 2) -> still closed: state=0, trips=0
	shouldFail = true
	req1 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req1.Header.Set("X-API-Key", testAPIKey)
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rr1.Code)
	}

	assertMetricContains("After 1 failure", "gateway_circuit_breaker_state 0")
	assertMetricContains("After 1 failure trips", "gateway_circuit_breaker_trips_total 0")

	// 3. 2nd failure (threshold reached) -> transitions to open: state=1, trips=1
	req2 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req2.Header.Set("X-API-Key", testAPIKey)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rr2.Code)
	}

	assertMetricContains("After threshold reached (open)", "gateway_circuit_breaker_state 1")
	assertMetricContains("After first trip", "gateway_circuit_breaker_trips_total 1")

	// 4. Request while open (fast fails with 503) -> still state=1, trips=1
	req3 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req3.Header.Set("X-API-Key", testAPIKey)
	rr3 := httptest.NewRecorder()
	handler.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr3.Code)
	}

	assertMetricContains("While open after 503", "gateway_circuit_breaker_state 1")
	assertMetricContains("While open trips remain 1", "gateway_circuit_breaker_trips_total 1")

	// 5. Advance clock past cooldown (10s) -> transitions to half-open: state=2, trips=1
	clock.Advance(10 * time.Second)
	assertMetricContains("Cooldown expired (half-open)", "gateway_circuit_breaker_state 2")
	assertMetricContains("Half-open trips remain 1", "gateway_circuit_breaker_trips_total 1")

	// 6. Trial probe succeeds -> transitions back to closed: state=0, trips=1
	shouldFail = false
	probeReq := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	probeReq.Header.Set("X-API-Key", testAPIKey)
	probeRR := httptest.NewRecorder()
	handler.ServeHTTP(probeRR, probeReq)
	if probeRR.Code != http.StatusOK {
		t.Fatalf("expected probe to return 200, got %d", probeRR.Code)
	}

	assertMetricContains("Probe succeeded (closed)", "gateway_circuit_breaker_state 0")
	assertMetricContains("Closed trips remain 1", "gateway_circuit_breaker_trips_total 1")

	// 7. Trip again (2 consecutive failures) -> transitions to open: state=1, trips=2
	shouldFail = true
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
	}

	assertMetricContains("Re-tripped to open", "gateway_circuit_breaker_state 1")
	assertMetricContains("Trips counter incremented to 2", "gateway_circuit_breaker_trips_total 2")

	// 8. Cooldown expires and probe fails -> reopens: state=1, trips=3
	clock.Advance(10 * time.Second)
	assertMetricContains("Second cooldown expired (half-open)", "gateway_circuit_breaker_state 2")

	// Probe fails
	probeFailReq := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	probeFailReq.Header.Set("X-API-Key", testAPIKey)
	probeFailRR := httptest.NewRecorder()
	handler.ServeHTTP(probeFailRR, probeFailReq)
	if probeFailRR.Code != http.StatusBadGateway {
		t.Fatalf("expected failed probe to return 502, got %d", probeFailRR.Code)
	}

	assertMetricContains("Failed probe reopens circuit", "gateway_circuit_breaker_state 1")
	assertMetricContains("Trips counter incremented to 3 on failed probe", "gateway_circuit_breaker_trips_total 3")
}

func TestParseBackendURLs(t *testing.T) {
	t.Run("MultipleURLsCommaSeparatedWithSpaces", func(t *testing.T) {
		urls, err := parseBackendURLs(" http://backend-1:8081 , http://backend-2:8082 ", "http://default:8081")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(urls) != 2 {
			t.Fatalf("expected 2 URLs, got %d", len(urls))
		}
		if urls[0].String() != "http://backend-1:8081" {
			t.Errorf("expected first URL 'http://backend-1:8081', got %q", urls[0].String())
		}
		if urls[1].String() != "http://backend-2:8082" {
			t.Errorf("expected second URL 'http://backend-2:8082', got %q", urls[1].String())
		}
	})

	t.Run("SingleURLInBackendURLs", func(t *testing.T) {
		urls, err := parseBackendURLs("http://backend-custom:9000", "http://default:8081")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(urls) != 1 || urls[0].String() != "http://backend-custom:9000" {
			t.Fatalf("expected ['http://backend-custom:9000'], got %v", urls)
		}
	})

	t.Run("FallbackToBackendURLWhenEmpty", func(t *testing.T) {
		urls, err := parseBackendURLs("", "http://fallback:8081")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(urls) != 1 || urls[0].String() != "http://fallback:8081" {
			t.Fatalf("expected ['http://fallback:8081'], got %v", urls)
		}
	})

	t.Run("FallbackWhitespaceOnly", func(t *testing.T) {
		urls, err := parseBackendURLs("   ", "http://fallback:8081")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(urls) != 1 || urls[0].String() != "http://fallback:8081" {
			t.Fatalf("expected ['http://fallback:8081'], got %v", urls)
		}
	})

	t.Run("ErrorWhenBothEmpty", func(t *testing.T) {
		_, err := parseBackendURLs("", "")
		if err == nil {
			t.Fatal("expected error when both BACKEND_URLS and fallback are empty")
		}
	})

	t.Run("ErrorOnMalformedURL", func(t *testing.T) {
		_, err := parseBackendURLs("://invalid-url", "http://default:8081")
		if err == nil {
			t.Fatal("expected error on malformed URL")
		}
	})

	t.Run("ErrorOnMissingHost", func(t *testing.T) {
		_, err := parseBackendURLs("http://", "http://default:8081")
		if err == nil {
			t.Fatal("expected error on URL without host")
		}
	})

	t.Run("ErrorOnMissingScheme", func(t *testing.T) {
		_, err := parseBackendURLs("backend-1:8081", "http://default:8081")
		if err == nil {
			t.Fatal("expected error on URL without scheme")
		}
	})
}

func TestBackendPool_RoundRobinAlternation(t *testing.T) {
	var count1, count2 atomic.Int64

	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count1.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"server":"s1"}`))
	}))
	defer s1.Close()

	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count2.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"server":"s2"}`))
	}))
	defer s2.Close()

	u1, _ := url.Parse(s1.URL)
	u2, _ := url.Parse(s2.URL)

	node1 := NewBackendNode(u1, NewCircuitBreaker(5, 10*time.Second), http.DefaultTransport)
	node2 := NewBackendNode(u2, NewCircuitBreaker(5, 10*time.Second), http.DefaultTransport)

	pool, err := NewBackendPool([]*BackendNode{node1, node2})
	if err != nil {
		t.Fatalf("failed to create backend pool: %v", err)
	}

	handler := newGatewayHandler(testAPIKey, pool, &mockLimiterAllowed{})

	expectedSequence := []string{`{"server":"s1"}`, `{"server":"s2"}`, `{"server":"s1"}`, `{"server":"s2"}`, `{"server":"s1"}`, `{"server":"s2"}`}

	for i, expected := range expectedSequence {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected status 200, got %d", i, rr.Code)
		}
		if rr.Body.String() != expected {
			t.Fatalf("request %d: expected body %q, got %q", i, expected, rr.Body.String())
		}
	}

	if count1.Load() != 3 {
		t.Errorf("expected server 1 to receive 3 requests, got %d", count1.Load())
	}
	if count2.Load() != 3 {
		t.Errorf("expected server 2 to receive 3 requests, got %d", count2.Load())
	}
}

func TestBackendPool_ConcurrentDistribution(t *testing.T) {
	var count1, count2 atomic.Int64

	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count1.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer s1.Close()

	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count2.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer s2.Close()

	u1, _ := url.Parse(s1.URL)
	u2, _ := url.Parse(s2.URL)

	node1 := NewBackendNode(u1, NewCircuitBreaker(5, 10*time.Second), http.DefaultTransport)
	node2 := NewBackendNode(u2, NewCircuitBreaker(5, 10*time.Second), http.DefaultTransport)

	pool, err := NewBackendPool([]*BackendNode{node1, node2})
	if err != nil {
		t.Fatalf("failed to create backend pool: %v", err)
	}

	handler := newGatewayHandler(testAPIKey, pool, &mockLimiterAllowed{})

	const totalRequests = 100
	var wg sync.WaitGroup
	errCh := make(chan error, totalRequests)

	for i := 0; i < totalRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
			req.Header.Set("X-API-Key", testAPIKey)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				errCh <- fmt.Errorf("expected 200, got %d", rr.Code)
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatal(err)
	}

	c1 := count1.Load()
	c2 := count2.Load()

	if c1+c2 != totalRequests {
		t.Fatalf("expected %d total requests, got %d", totalRequests, c1+c2)
	}
	if c1 != totalRequests/2 || c2 != totalRequests/2 {
		t.Errorf("expected perfectly balanced 50/50 distribution, got %d and %d", c1, c2)
	}
}

func TestBackendPool_OneBackendFailingWhileOtherHealthy(t *testing.T) {
	var count2 atomic.Int64

	// Backend 1 uses a transport that always fails
	fakeFailingTransport := &fakeRoundTripper{
		onCall: func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("backend 1 connection refused")
		},
	}

	// Backend 2 is healthy
	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count2.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"server":"s2"}`))
	}))
	defer s2.Close()

	u1, _ := url.Parse("http://backend-1.internal:8081")
	u2, _ := url.Parse(s2.URL)

	// Threshold 2: 2 consecutive failures trip backend 1
	cb1 := NewCircuitBreaker(2, 10*time.Second)
	cb2 := NewCircuitBreaker(2, 10*time.Second)

	node1 := NewBackendNode(u1, cb1, fakeFailingTransport)
	node2 := NewBackendNode(u2, cb2, http.DefaultTransport)

	pool, err := NewBackendPool([]*BackendNode{node1, node2})
	if err != nil {
		t.Fatalf("failed to create backend pool: %v", err)
	}

	handler := newGatewayHandler(testAPIKey, pool, &mockLimiterAllowed{})

	// Request 1: hits Backend 1 -> fails with 502 (consecutive failures on cb1 = 1)
	req1 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req1.Header.Set("X-API-Key", testAPIKey)
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusBadGateway {
		t.Fatalf("expected req 1 to return 502, got %d", rr1.Code)
	}
	if cb1.State() != StateClosed {
		t.Fatalf("expected cb1 to remain closed after 1 failure, got %v", cb1.State())
	}

	// Request 2: hits Backend 2 -> succeeds with 200
	req2 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req2.Header.Set("X-API-Key", testAPIKey)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected req 2 to return 200, got %d", rr2.Code)
	}

	// Request 3: hits Backend 1 -> fails with 502 (consecutive failures on cb1 = 2 -> trips to OPEN!)
	req3 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req3.Header.Set("X-API-Key", testAPIKey)
	rr3 := httptest.NewRecorder()
	handler.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusBadGateway {
		t.Fatalf("expected req 3 to return 502, got %d", rr3.Code)
	}
	if cb1.State() != StateOpen {
		t.Fatalf("expected cb1 to trip to Open after 2 failures, got %v", cb1.State())
	}
	if cb2.State() != StateClosed {
		t.Fatalf("expected cb2 to remain Closed, got %v", cb2.State())
	}

	// Now that Backend 1 is OPEN, all subsequent requests must skip Backend 1 and succeed via Backend 2
	for i := 0; i < 6; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d while cb1 is open: expected 200 from healthy backend 2, got %d", i, rr.Code)
		}
		if rr.Body.String() != `{"server":"s2"}` {
			t.Fatalf("request %d: expected response from s2, got %q", i, rr.Body.String())
		}
	}

	if cb1.State() != StateOpen {
		t.Errorf("cb1 should still be open, got %v", cb1.State())
	}
	if cb2.State() != StateClosed {
		t.Errorf("cb2 should still be closed, got %v", cb2.State())
	}
}

func TestBackendPool_AllBackendsFailing(t *testing.T) {
	fakeFailingTransport := &fakeRoundTripper{
		onCall: func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("upstream connection dropped")
		},
	}

	u1, _ := url.Parse("http://backend-1.internal:8081")
	u2, _ := url.Parse("http://backend-2.internal:8081")

	// Threshold 1: 1 failure trips each backend
	cb1 := NewCircuitBreaker(1, 15*time.Second)
	cb2 := NewCircuitBreaker(1, 15*time.Second)

	node1 := NewBackendNode(u1, cb1, fakeFailingTransport)
	node2 := NewBackendNode(u2, cb2, fakeFailingTransport)

	pool, err := NewBackendPool([]*BackendNode{node1, node2})
	if err != nil {
		t.Fatalf("failed to create backend pool: %v", err)
	}

	handler := newGatewayHandler(testAPIKey, pool, &mockLimiterAllowed{})

	// Request 1: hits Backend 1 -> fails with 502, trips cb1 to Open
	req1 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req1.Header.Set("X-API-Key", testAPIKey)
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusBadGateway {
		t.Fatalf("expected req 1 to return 502, got %d", rr1.Code)
	}

	// Request 2: hits Backend 2 -> fails with 502, trips cb2 to Open
	req2 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req2.Header.Set("X-API-Key", testAPIKey)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusBadGateway {
		t.Fatalf("expected req 2 to return 502, got %d", rr2.Code)
	}

	if cb1.State() != StateOpen || cb2.State() != StateOpen {
		t.Fatalf("expected both circuit breakers to be open, got cb1=%v, cb2=%v", cb1.State(), cb2.State())
	}

	// Request 3: All backends are OPEN. Must return HTTP 503 Service Unavailable with Retry-After
	req3 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req3.Header.Set("X-API-Key", testAPIKey)
	rr3 := httptest.NewRecorder()
	handler.ServeHTTP(rr3, req3)

	if rr3.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable when all backends open, got %d", rr3.Code)
	}

	retryAfter := rr3.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("expected Retry-After header on 503 response, got none")
	}
	retryVal, err := strconv.Atoi(retryAfter)
	if err != nil || retryVal <= 0 {
		t.Fatalf("expected positive integer Retry-After header, got %q", retryAfter)
	}

	if !strings.Contains(rr3.Body.String(), "circuit breaker is open") {
		t.Fatalf("expected body to mention circuit breaker is open, got %q", rr3.Body.String())
	}
}

func TestBackendPool_RecoveryAfterCooldown(t *testing.T) {
	clock := newFakeClock(time.Now())
	var b1ShouldFail bool

	fakeB1Transport := &fakeRoundTripper{
		onCall: func(req *http.Request) (*http.Response, error) {
			if b1ShouldFail {
				return nil, errors.New("b1 connection error")
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader(`{"server":"s1"}`)),
				ContentLength: 15,
				Request:       req,
			}, nil
		},
	}

	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"server":"s2"}`))
	}))
	defer s2.Close()

	u1, _ := url.Parse("http://backend-1.internal:8081")
	u2, _ := url.Parse(s2.URL)

	// Threshold 1, Cooldown 10s
	cb1 := NewCircuitBreaker(1, 10*time.Second, clock.Now)
	cb2 := NewCircuitBreaker(1, 10*time.Second, clock.Now)

	node1 := NewBackendNode(u1, cb1, fakeB1Transport)
	node2 := NewBackendNode(u2, cb2, http.DefaultTransport)

	pool, err := NewBackendPool([]*BackendNode{node1, node2})
	if err != nil {
		t.Fatalf("failed to create backend pool: %v", err)
	}

	handler := newGatewayHandler(testAPIKey, pool, &mockLimiterAllowed{})

	// 1. Trip Backend 1 to OPEN
	b1ShouldFail = true
	req1 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req1.Header.Set("X-API-Key", testAPIKey)
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rr1.Code)
	}
	if cb1.State() != StateOpen {
		t.Fatalf("expected cb1 to be Open, got %v", cb1.State())
	}

	// 2. While cooldown is active, requests go to Backend 2
	req2 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req2.Header.Set("X-API-Key", testAPIKey)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK || rr2.Body.String() != `{"server":"s2"}` {
		t.Fatalf("expected s2 response, got code=%d body=%s", rr2.Code, rr2.Body.String())
	}

	// 3. Advance clock past cooldown (10s), heal backend 1
	clock.Advance(10 * time.Second)
	b1ShouldFail = false

	// Next request sends trial probe to Backend 1
	probeReq := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	probeReq.Header.Set("X-API-Key", testAPIKey)
	probeRR := httptest.NewRecorder()
	handler.ServeHTTP(probeRR, probeReq)

	if probeRR.Code != http.StatusOK {
		t.Fatalf("expected probe to return 200, got %d", probeRR.Code)
	}
	if cb1.State() != StateClosed {
		t.Fatalf("expected cb1 to transition back to Closed after successful probe, got %v", cb1.State())
	}

	// 4. Normal round-robin resumes between both backends
	reqA := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	reqA.Header.Set("X-API-Key", testAPIKey)
	rrA := httptest.NewRecorder()
	handler.ServeHTTP(rrA, reqA)
	if rrA.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rrA.Code)
	}

	reqB := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	reqB.Header.Set("X-API-Key", testAPIKey)
	rrB := httptest.NewRecorder()
	handler.ServeHTTP(rrB, reqB)
	if rrB.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rrB.Code)
	}
}

func TestBackendPool_Authentication(t *testing.T) {
	var count atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer s.Close()

	u, _ := url.Parse(s.URL)
	node := NewBackendNode(u, NewCircuitBreaker(5, 10*time.Second), http.DefaultTransport)
	pool, _ := NewBackendPool([]*BackendNode{node})

	handler := newGatewayHandler(testAPIKey, pool, &mockLimiterAllowed{})

	// Missing API key
	reqMissing := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	rrMissing := httptest.NewRecorder()
	handler.ServeHTTP(rrMissing, reqMissing)
	if rrMissing.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing API key, got %d", rrMissing.Code)
	}

	// Invalid API key
	reqInvalid := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	reqInvalid.Header.Set("X-API-Key", "wrong-api-key")
	rrInvalid := httptest.NewRecorder()
	handler.ServeHTTP(rrInvalid, reqInvalid)
	if rrInvalid.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong API key, got %d", rrInvalid.Code)
	}

	// Verify no requests reached the backend
	if count.Load() != 0 {
		t.Fatalf("expected 0 requests to reach backend on unauthenticated calls, got %d", count.Load())
	}
}

type mockLimiterCounting struct {
	mu         sync.Mutex
	count      int64
	limit      int64
	retryAfter int64
}

func (m *mockLimiterCounting) Allow(ctx context.Context, identity string) (RateLimitResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.count++
	if m.count > m.limit {
		return RateLimitResult{
			Allowed:    false,
			Limit:      m.limit,
			Remaining:  0,
			RetryAfter: m.retryAfter,
		}, nil
	}
	return RateLimitResult{
		Allowed:    true,
		Limit:      m.limit,
		Remaining:  m.limit - m.count,
		RetryAfter: 0,
	}, nil
}

func TestBackendPool_RateLimiting(t *testing.T) {
	var count atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer s.Close()

	u, _ := url.Parse(s.URL)
	node := NewBackendNode(u, NewCircuitBreaker(5, 10*time.Second), http.DefaultTransport)
	pool, _ := NewBackendPool([]*BackendNode{node})

	// Rate limiter permits first 2 requests, rejects subsequent ones
	limiter := &mockLimiterCounting{limit: 2, retryAfter: 30}
	handler := newGatewayHandler(testAPIKey, pool, limiter)

	// First 2 requests succeed
	for i := 1; i <= 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, rr.Code)
		}
	}

	// 3rd and 4th requests are rate-limited
	for i := 3; i <= 4; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusTooManyRequests {
			t.Fatalf("request %d: expected 429, got %d", i, rr.Code)
		}
		if rr.Header().Get("Retry-After") != "30" {
			t.Fatalf("request %d: expected Retry-After 30, got %q", i, rr.Header().Get("Retry-After"))
		}
	}

	if count.Load() != 2 {
		t.Fatalf("expected exactly 2 requests to reach backend, got %d", count.Load())
	}
}

func TestBackendPool_PerBackendMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewGatewayMetrics(reg)

	var shouldFailB1 bool
	fakeB1 := &fakeRoundTripper{
		onCall: func(req *http.Request) (*http.Response, error) {
			if shouldFailB1 {
				return nil, errors.New("b1 failure")
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

	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer s2.Close()

	u1, _ := url.Parse("http://b1.internal:8081")
	u2, _ := url.Parse(s2.URL)

	cb1 := NewCircuitBreaker(1, 10*time.Second)
	cb2 := NewCircuitBreaker(1, 10*time.Second)

	node1 := NewBackendNode(u1, cb1, fakeB1)
	node2 := NewBackendNode(u2, cb2, http.DefaultTransport)

	pool, _ := NewBackendPool([]*BackendNode{node1, node2})
	pool.SetMetrics(metrics)

	handler := newGatewayHandler(testAPIKey, pool, &mockLimiterAllowed{}, metrics)

	scrape := func() string {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr.Body.String()
	}

	body := scrape()
	// Both backends initialized to state 0
	if !strings.Contains(body, `gateway_backend_circuit_breaker_state{backend="b1.internal:8081"} 0`) {
		t.Fatalf("expected b1 initial metric 0, got:\n%s", body)
	}
	if !strings.Contains(body, `gateway_backend_circuit_breaker_state{backend="`+u2.Host+`"} 0`) {
		t.Fatalf("expected b2 initial metric 0, got:\n%s", body)
	}

	// Trip b1
	shouldFailB1 = true
	req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req.Header.Set("X-API-Key", testAPIKey)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rr.Code)
	}

	bodyAfter := scrape()
	// b1 is now open (state 1, trips 1)
	if !strings.Contains(bodyAfter, `gateway_backend_circuit_breaker_state{backend="b1.internal:8081"} 1`) {
		t.Fatalf("expected b1 state 1 after trip, got:\n%s", bodyAfter)
	}
	if !strings.Contains(bodyAfter, `gateway_backend_circuit_breaker_trips_total{backend="b1.internal:8081"} 1`) {
		t.Fatalf("expected b1 trips 1 after trip, got:\n%s", bodyAfter)
	}
	// b2 is still closed (state 0, trips 0)
	if !strings.Contains(bodyAfter, `gateway_backend_circuit_breaker_state{backend="`+u2.Host+`"} 0`) {
		t.Fatalf("expected b2 state 0, got:\n%s", bodyAfter)
	}
	if !strings.Contains(bodyAfter, `gateway_backend_circuit_breaker_trips_total{backend="`+u2.Host+`"} 0`) {
		t.Fatalf("expected b2 trips 0, got:\n%s", bodyAfter)
	}
}

func TestBackendPool_BackendIDRoundRobinDistribution(t *testing.T) {
	// Create two real backend instances simulating backend-1 and backend-2 with BACKEND_ID
	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Backend-Id", "backend-1")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"backend_id":"backend-1","message":"Hello from backend","students":["Arjun","Priya","Rahul"]}`))
	}))
	defer s1.Close()

	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Backend-Id", "backend-2")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"backend_id":"backend-2","message":"Hello from backend","students":["Arjun","Priya","Rahul"]}`))
	}))
	defer s2.Close()

	u1, _ := url.Parse(s1.URL)
	u2, _ := url.Parse(s2.URL)

	node1 := NewBackendNode(u1, NewCircuitBreaker(5, 10*time.Second), http.DefaultTransport)
	node2 := NewBackendNode(u2, NewCircuitBreaker(5, 10*time.Second), http.DefaultTransport)

	pool, err := NewBackendPool([]*BackendNode{node1, node2})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}

	handler := newGatewayHandler(testAPIKey, pool, &mockLimiterAllowed{})

	expectedBackendIDs := []string{"backend-1", "backend-2", "backend-1", "backend-2"}

	for i, expectedID := range expectedBackendIDs {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200 OK, got %d", i, rr.Code)
		}

		if gotHeader := rr.Header().Get("X-Backend-Id"); gotHeader != expectedID {
			t.Errorf("request %d: expected X-Backend-Id %q, got %q", i, expectedID, gotHeader)
		}

		var resp map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("request %d: failed to unmarshal response: %v", i, err)
		}

		if resp["backend_id"] != expectedID {
			t.Errorf("request %d: expected backend_id %q, got %v", i, expectedID, resp["backend_id"])
		}
		if resp["message"] != "Hello from backend" {
			t.Errorf("request %d: expected message 'Hello from backend', got %v", i, resp["message"])
		}
	}
}

// TestBackendPool_FailoverWithRateLimitingAndAllBackendsOpen verifies:
// 1. A failed backend whose circuit breaker opens is skipped while a healthy alternative exists.
// 2. Gateway rate limiting (HTTP 429) is never treated as an upstream transport failure.
// 3. Rate limiting does not alter circuit breaker state or failure counts for any backend.
// 4. When all backends in the pool are open, requests fail fast with 503 and Retry-After.
func TestBackendPool_FailoverWithRateLimitingAndAllBackendsOpen(t *testing.T) {
	var (
		b1Calls atomic.Int64
		b2Calls atomic.Int64
		b2Fail  atomic.Bool
	)

	fakeB1Transport := &fakeRoundTripper{
		onCall: func(req *http.Request) (*http.Response, error) {
			b1Calls.Add(1)
			return nil, errors.New("backend-1 connection refused")
		},
	}

	fakeB2Transport := &fakeRoundTripper{
		onCall: func(req *http.Request) (*http.Response, error) {
			b2Calls.Add(1)
			if b2Fail.Load() {
				return nil, errors.New("backend-2 connection dropped")
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader(`{"backend_id":"backend-2"}`)),
				ContentLength: 26,
				Request:       req,
			}, nil
		},
	}

	u1, _ := url.Parse("http://backend-1.internal:8081")
	u2, _ := url.Parse("http://backend-2.internal:8081")

	// Threshold 1: 1 failure trips backend-1 into StateOpen
	cb1 := NewCircuitBreaker(1, 15*time.Second)
	// Threshold 1: 1 failure trips backend-2 into StateOpen
	cb2 := NewCircuitBreaker(1, 15*time.Second)

	node1 := NewBackendNode(u1, cb1, fakeB1Transport)
	node2 := NewBackendNode(u2, cb2, fakeB2Transport)

	pool, err := NewBackendPool([]*BackendNode{node1, node2})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}

	// Rate limiter allows first 3 requests, then returns 429 with Retry-After 45
	limiter := &mockLimiterCounting{limit: 3, retryAfter: 45}
	handler := newGatewayHandler(testAPIKey, pool, limiter)

	// Step 1: Request 1 hits Backend 1 -> fails with 502 -> trips cb1 to Open
	req1 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req1.Header.Set("X-API-Key", testAPIKey)
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, req1)

	if rr1.Code != http.StatusBadGateway {
		t.Fatalf("expected req 1 to return 502, got %d", rr1.Code)
	}
	if node1.State() != StateOpen {
		t.Fatalf("expected node1 to be StateOpen after 1 failure, got %v", node1.State())
	}
	if node1.IsAvailable() {
		t.Fatal("expected node1 to be unavailable for selection when breaker is open")
	}
	if node2.State() != StateClosed {
		t.Fatalf("expected node2 to remain StateClosed, got %v", node2.State())
	}

	// Step 2: Request 2 - load balancer avoids open Backend 1 and routes to healthy Backend 2
	req2 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req2.Header.Set("X-API-Key", testAPIKey)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("expected req 2 to succeed via backend-2 with 200, got %d", rr2.Code)
	}
	if !strings.Contains(rr2.Body.String(), "backend-2") {
		t.Fatalf("expected req 2 to hit backend-2, got %s", rr2.Body.String())
	}

	// Step 3: Request 3 - Backend 1 remains skipped, routes to Backend 2 (3rd allowed request)
	req3 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req3.Header.Set("X-API-Key", testAPIKey)
	rr3 := httptest.NewRecorder()
	handler.ServeHTTP(rr3, req3)

	if rr3.Code != http.StatusOK {
		t.Fatalf("expected req 3 to succeed via backend-2 with 200, got %d", rr3.Code)
	}

	// Step 4: Request 4 - Rate limit is exceeded (3 requests consumed).
	// Must return HTTP 429 Too Many Requests with Retry-After: 45.
	req4 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req4.Header.Set("X-API-Key", testAPIKey)
	rr4 := httptest.NewRecorder()
	handler.ServeHTTP(rr4, req4)

	if rr4.Code != http.StatusTooManyRequests {
		t.Fatalf("expected req 4 to return 429, got %d", rr4.Code)
	}
	if rr4.Header().Get("Retry-After") != "45" {
		t.Fatalf("expected Retry-After 45, got %q", rr4.Header().Get("Retry-After"))
	}

	// Verify that HTTP 429 was NOT treated as an upstream transport failure:
	// - cb2 must STILL be StateClosed with 0 failures
	// - cb1 must STILL be StateOpen with 1 failure
	// - neither backend transport received an additional request during the 429 rejection
	if node2.State() != StateClosed {
		t.Fatalf("expected node2 state to remain Closed after 429, got %v", node2.State())
	}
	if node2.ConsecutiveFailures() != 0 {
		t.Fatalf("expected node2 consecutive failures to be 0 after 429, got %d", node2.ConsecutiveFailures())
	}
	if node1.State() != StateOpen {
		t.Fatalf("expected node1 state to remain Open after 429, got %v", node1.State())
	}
	if node1.ConsecutiveFailures() != 1 {
		t.Fatalf("expected node1 consecutive failures to remain 1 after 429, got %d", node1.ConsecutiveFailures())
	}
	if b1Calls.Load() != 1 {
		t.Fatalf("expected b1 calls to remain 1, got %d", b1Calls.Load())
	}
	if b2Calls.Load() != 2 {
		t.Fatalf("expected b2 calls to remain 2, got %d", b2Calls.Load())
	}

	// Step 5: Increase rate limit quota to allow further verification
	limiter.mu.Lock()
	limiter.limit = 10
	limiter.mu.Unlock()

	// Request 5 succeeds via Backend 2, proving healthy backend is completely unaffected
	req5 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req5.Header.Set("X-API-Key", testAPIKey)
	rr5 := httptest.NewRecorder()
	handler.ServeHTTP(rr5, req5)

	if rr5.Code != http.StatusOK {
		t.Fatalf("expected req 5 to return 200, got %d", rr5.Code)
	}

	// Step 6: Backend 2 now fails as well -> trips cb2 to StateOpen
	b2Fail.Store(true)
	req6 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req6.Header.Set("X-API-Key", testAPIKey)
	rr6 := httptest.NewRecorder()
	handler.ServeHTTP(rr6, req6)

	if rr6.Code != http.StatusBadGateway {
		t.Fatalf("expected req 6 to return 502 Bad Gateway on upstream failure, got %d", rr6.Code)
	}
	if node2.State() != StateOpen {
		t.Fatalf("expected node2 to trip to StateOpen, got %v", node2.State())
	}

	// Step 7: ALL backends in the pool are now OPEN.
	// All subsequent requests must return HTTP 503 Service Unavailable with Retry-After.
	for i := 0; i < 3; i++ {
		reqAllOpen := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		reqAllOpen.Header.Set("X-API-Key", testAPIKey)
		rrAllOpen := httptest.NewRecorder()
		handler.ServeHTTP(rrAllOpen, reqAllOpen)

		if rrAllOpen.Code != http.StatusServiceUnavailable {
			t.Fatalf("request %d while all open: expected 503 Service Unavailable, got %d", i, rrAllOpen.Code)
		}
		retryAfter := rrAllOpen.Header().Get("Retry-After")
		if retryAfter == "" {
			t.Fatalf("request %d while all open: expected Retry-After header, got none", i)
		}
		retryVal, err := strconv.Atoi(retryAfter)
		if err != nil || retryVal <= 0 {
			t.Fatalf("request %d while all open: expected positive Retry-After, got %q", i, retryAfter)
		}
		if !strings.Contains(rrAllOpen.Body.String(), "circuit breaker is open") {
			t.Fatalf("request %d while all open: expected circuit breaker open body, got %q", i, rrAllOpen.Body.String())
		}
	}
}

// TestBackendPool_PerBackendCircuitBreakerLifecycle verifies the full lifecycle:
// 1. One backend fails while another remains healthy;
// 2. Repeated transport failures open only the failing backend's breaker;
// 3. All-open backends return 503 with Retry-After;
// 4. Recovery after cooldown works via trial probes.
func TestBackendPool_PerBackendCircuitBreakerLifecycle(t *testing.T) {
	clock := newFakeClock(time.Now())

	var (
		b1Fail atomic.Bool
		b2Fail atomic.Bool
		b1Hits atomic.Int64
		b2Hits atomic.Int64
	)

	fakeB1 := &fakeRoundTripper{
		onCall: func(req *http.Request) (*http.Response, error) {
			b1Hits.Add(1)
			if b1Fail.Load() {
				return nil, errors.New("backend-1 transport down")
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader(`{"server":"b1"}`)),
				ContentLength: 15,
				Request:       req,
			}, nil
		},
	}

	fakeB2 := &fakeRoundTripper{
		onCall: func(req *http.Request) (*http.Response, error) {
			b2Hits.Add(1)
			if b2Fail.Load() {
				return nil, errors.New("backend-2 transport down")
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader(`{"server":"b2"}`)),
				ContentLength: 15,
				Request:       req,
			}, nil
		},
	}

	u1, _ := url.Parse("http://backend-1.internal:8081")
	u2, _ := url.Parse("http://backend-2.internal:8081")

	// Threshold 5 (matching production default), 10s cooldown, fake clock
	cb1 := NewCircuitBreaker(5, 10*time.Second, clock.Now)
	cb2 := NewCircuitBreaker(5, 10*time.Second, clock.Now)

	node1 := NewBackendNode(u1, cb1, fakeB1)
	node2 := NewBackendNode(u2, cb2, fakeB2)

	pool, err := NewBackendPool([]*BackendNode{node1, node2})
	if err != nil {
		t.Fatalf("failed to create backend pool: %v", err)
	}

	// Use mockLimiterAllowed for completely isolated test without hitting shared Redis
	handler := newGatewayHandler(testAPIKey, pool, &mockLimiterAllowed{})

	// 1. Initial healthy state: round-robin alternates between b1 and b2
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("initial request %d failed: %d", i, rr.Code)
		}
	}
	if b1Hits.Load() != 2 || b2Hits.Load() != 2 {
		t.Fatalf("expected 2 hits to b1 and 2 hits to b2, got b1=%d b2=%d", b1Hits.Load(), b2Hits.Load())
	}

	// 2. Make Backend 1 start failing. Backend 2 remains healthy.
	b1Fail.Store(true)

	// Send 5 failures to Backend 1 while Backend 2 succeeds
	// Round-robin alternating:
	// b1 fails (failure 1), b2 succeeds
	// b1 fails (failure 2), b2 succeeds
	// b1 fails (failure 3), b2 succeeds
	// b1 fails (failure 4), b2 succeeds
	// b1 fails (failure 5 -> trips to StateOpen!), b2 succeeds
	for i := 1; i <= 5; i++ {
		// Request to b1: fails with 502
		reqB1 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		reqB1.Header.Set("X-API-Key", testAPIKey)
		rrB1 := httptest.NewRecorder()
		handler.ServeHTTP(rrB1, reqB1)
		if rrB1.Code != http.StatusBadGateway {
			t.Fatalf("iteration %d: expected b1 to return 502, got %d", i, rrB1.Code)
		}

		if i < 5 {
			if node1.State() != StateClosed {
				t.Fatalf("iteration %d: expected b1 to remain closed before threshold 5, got %v", i, node1.State())
			}
			if node1.ConsecutiveFailures() != i {
				t.Fatalf("iteration %d: expected b1 consecutive failures %d, got %d", i, i, node1.ConsecutiveFailures())
			}
		}

		// Request to b2: succeeds with 200
		reqB2 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		reqB2.Header.Set("X-API-Key", testAPIKey)
		rrB2 := httptest.NewRecorder()
		handler.ServeHTTP(rrB2, reqB2)
		if rrB2.Code != http.StatusOK {
			t.Fatalf("iteration %d: expected b2 to return 200, got %d", i, rrB2.Code)
		}
		if node2.State() != StateClosed || node2.ConsecutiveFailures() != 0 {
			t.Fatalf("iteration %d: b2 must remain healthy and closed with 0 failures", i)
		}
	}

	// Verify only failing backend's breaker opened
	if node1.State() != StateOpen {
		t.Fatalf("expected node1 to be StateOpen after 5 failures, got %v", node1.State())
	}
	if node1.IsAvailable() {
		t.Fatal("expected node1 to be unavailable when StateOpen")
	}
	if node2.State() != StateClosed {
		t.Fatalf("expected node2 to remain StateClosed, got %v", node2.State())
	}
	if !node2.IsAvailable() {
		t.Fatal("expected node2 to remain available")
	}

	// 3. One backend failed while another remains healthy:
	// All subsequent requests skip open Backend 1 and succeed via healthy Backend 2
	b2HitsBefore := b2Hits.Load()
	b1HitsBefore := b1Hits.Load()
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d while b1 is open: expected 200 from b2, got %d", i, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "b2") {
			t.Fatalf("request %d: expected response from b2, got %q", i, rr.Body.String())
		}
	}
	if b1Hits.Load() != b1HitsBefore {
		t.Fatalf("expected no additional calls to b1 while open, got %d (was %d)", b1Hits.Load(), b1HitsBefore)
	}
	if b2Hits.Load() != b2HitsBefore+10 {
		t.Fatalf("expected 10 calls to b2, got %d", b2Hits.Load()-b2HitsBefore)
	}

	// 4. Advance clock by 5s, then make Backend 2 fail until its breaker trips to StateOpen at T=5s
	clock.Advance(5 * time.Second)
	b2Fail.Store(true)
	for i := 1; i <= 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("expected b2 failure %d to return 502, got %d", i, rr.Code)
		}
	}
	if node2.State() != StateOpen {
		t.Fatalf("expected node2 to be StateOpen after 5 failures, got %v", node2.State())
	}

	// 5. All-open backends: both backends are open at T=5s
	// Requests must fast-fail with HTTP 503 and Retry-After
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("request %d while all open: expected 503, got %d", i, rr.Code)
		}
		retryAfter := rr.Header().Get("Retry-After")
		if retryAfter == "" {
			t.Fatalf("request %d while all open: expected Retry-After header, got none", i)
		}
		retrySecs, err := strconv.Atoi(retryAfter)
		if err != nil || retrySecs <= 0 {
			t.Fatalf("request %d while all open: expected positive integer Retry-After, got %q", i, retryAfter)
		}
		if !strings.Contains(rr.Body.String(), "circuit breaker is open") {
			t.Fatalf("request %d while all open: expected circuit breaker open message, got %q", i, rr.Body.String())
		}
	}

	// 6. Recovery after cooldown:
	// Advance clock by 5s to T=10s.
	// Backend 1 (opened at T=0) has elapsed its 10s cooldown -> ready for trial probe.
	// Backend 2 (opened at T=5s) has only elapsed 5s -> still strictly OPEN.
	clock.Advance(5 * time.Second)
	b1Fail.Store(false) // Heal Backend 1

	// Next request sends trial probe to Backend 1 (Backend 2 is still Open)
	probeReq := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	probeReq.Header.Set("X-API-Key", testAPIKey)
	probeRR := httptest.NewRecorder()
	handler.ServeHTTP(probeRR, probeReq)

	if probeRR.Code != http.StatusOK {
		t.Fatalf("expected probe to return 200, got %d", probeRR.Code)
	}
	if !strings.Contains(probeRR.Body.String(), "b1") {
		t.Fatalf("expected probe response from b1, got %q", probeRR.Body.String())
	}
	if node1.State() != StateClosed {
		t.Fatalf("expected node1 to close after successful probe, got %v", node1.State())
	}
	if node1.ConsecutiveFailures() != 0 {
		t.Fatalf("expected node1 consecutive failures to reset to 0, got %d", node1.ConsecutiveFailures())
	}

	// While Backend 2 is still open, requests route to recovered Backend 1
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "b1") {
			t.Fatalf("expected 200 OK from recovered b1, got code=%d body=%s", rr.Code, rr.Body.String())
		}
	}

	// Now advance clock another 5s to T=15s (Backend 2 has now elapsed 10s cooldown)
	clock.Advance(5 * time.Second)
	b2Fail.Store(false)

	// Probe Backend 2 -> succeeds and closes
	probeReq2 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	probeReq2.Header.Set("X-API-Key", testAPIKey)
	probeRR2 := httptest.NewRecorder()
	handler.ServeHTTP(probeRR2, probeReq2)
	if probeRR2.Code != http.StatusOK {
		t.Fatalf("expected probe 2 to return 200, got %d", probeRR2.Code)
	}
	if node2.State() != StateClosed {
		t.Fatalf("expected node2 to close after successful probe, got %v", node2.State())
	}

	// Both backends are closed now: full round-robin alternation resumes
	expectedSeq := []string{"b1", "b2", "b1", "b2"}
	for i, expected := range expectedSeq {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("resumed request %d: expected 200, got %d", i, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), expected) {
			t.Fatalf("resumed request %d: expected server %s, got %s", i, expected, rr.Body.String())
		}
	}
}

// TestBackendPool_CircuitBreakerIsolationAndFailover validates:
// 1. Strict per-backend circuit breaker isolation (failures on one backend never affect the other).
// 2. Seamless failover to a healthy backend when one backend trips to open.
// 3. Fast-fail HTTP 503 with Retry-After when all backends in the pool are open.
func TestBackendPool_CircuitBreakerIsolationAndFailover(t *testing.T) {
	var (
		b1Calls atomic.Int64
		b2Calls atomic.Int64
		b1Fail  atomic.Bool
		b2Fail  atomic.Bool
	)

	fake1 := &fakeRoundTripper{
		onCall: func(req *http.Request) (*http.Response, error) {
			b1Calls.Add(1)
			if b1Fail.Load() {
				return nil, errors.New("backend 1 network unreachable")
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader(`{"server":"b1"}`)),
				ContentLength: 15,
				Request:       req,
			}, nil
		},
	}

	fake2 := &fakeRoundTripper{
		onCall: func(req *http.Request) (*http.Response, error) {
			b2Calls.Add(1)
			if b2Fail.Load() {
				return nil, errors.New("backend 2 network unreachable")
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader(`{"server":"b2"}`)),
				ContentLength: 15,
				Request:       req,
			}, nil
		},
	}

	u1, _ := url.Parse("http://backend-1.internal:8081")
	u2, _ := url.Parse("http://backend-2.internal:8081")

	// Threshold 3 for both backends
	cb1 := NewCircuitBreaker(3, 10*time.Second)
	cb2 := NewCircuitBreaker(3, 10*time.Second)

	node1 := NewBackendNode(u1, cb1, fake1)
	node2 := NewBackendNode(u2, cb2, fake2)

	pool, err := NewBackendPool([]*BackendNode{node1, node2})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}

	handler := newGatewayHandler(testAPIKey, pool, &mockLimiterAllowed{})

	// 1. Initial healthy state: verify both are closed with 0 failures
	if node1.State() != StateClosed || node1.ConsecutiveFailures() != 0 {
		t.Fatalf("expected node1 initially closed with 0 failures")
	}
	if node2.State() != StateClosed || node2.ConsecutiveFailures() != 0 {
		t.Fatalf("expected node2 initially closed with 0 failures")
	}

	// 2. Make backend 1 fail
	b1Fail.Store(true)

	// Trip backend 1 (requires 3 consecutive failures)
	// Round robin will alternate between b1 (fails) and b2 (succeeds)
	for i := 1; i <= 3; i++ {
		// Request to b1 -> 502
		req1 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req1.Header.Set("X-API-Key", testAPIKey)
		rr1 := httptest.NewRecorder()
		handler.ServeHTTP(rr1, req1)
		if rr1.Code != http.StatusBadGateway {
			t.Fatalf("expected b1 request to return 502, got %d", rr1.Code)
		}

		// Request to b2 -> 200
		req2 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req2.Header.Set("X-API-Key", testAPIKey)
		rr2 := httptest.NewRecorder()
		handler.ServeHTTP(rr2, req2)
		if rr2.Code != http.StatusOK {
			t.Fatalf("expected b2 request to return 200, got %d", rr2.Code)
		}

		// Isolation check: backend 2 must remain closed with 0 failures
		if node2.State() != StateClosed || node2.ConsecutiveFailures() != 0 {
			t.Fatalf("iteration %d: backend 2 circuit breaker was affected by backend 1 failure!", i)
		}
	}

	// Backend 1 must now be OPEN
	if node1.State() != StateOpen {
		t.Fatalf("expected node1 to be StateOpen after 3 failures, got %v", node1.State())
	}
	if node1.IsAvailable() {
		t.Fatal("expected node1 to be marked unavailable when open")
	}

	// Backend 2 must remain CLOSED
	if node2.State() != StateClosed || !node2.IsAvailable() {
		t.Fatalf("expected node2 to remain StateClosed and available, got %v", node2.State())
	}

	// 3. Failover: all subsequent requests must route to healthy backend 2
	b2CallsBefore := b2Calls.Load()
	b1CallsBefore := b1Calls.Load()
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("failover request %d: expected 200 OK from healthy backend 2, got %d", i, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "b2") {
			t.Fatalf("failover request %d: expected response from b2, got %q", i, rr.Body.String())
		}
	}

	// Verify no additional requests reached failed backend 1
	if b1Calls.Load() != b1CallsBefore {
		t.Fatalf("expected 0 additional requests to b1 while open, got %d", b1Calls.Load()-b1CallsBefore)
	}
	if b2Calls.Load() != b2CallsBefore+5 {
		t.Fatalf("expected 5 requests to b2, got %d", b2Calls.Load()-b2CallsBefore)
	}

	// 4. All-backends-open: now trip backend 2 as well
	b2Fail.Store(true)
	for i := 1; i <= 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("expected b2 trip request %d to return 502, got %d", i, rr.Code)
		}
	}

	if node2.State() != StateOpen {
		t.Fatalf("expected node2 to be StateOpen after 3 failures, got %v", node2.State())
	}

	// Both backends are now OPEN. Requests must return HTTP 503 with Retry-After.
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("all-open request %d: expected 503 Service Unavailable, got %d", i, rr.Code)
		}
		retryAfter := rr.Header().Get("Retry-After")
		if retryAfter == "" {
			t.Fatalf("all-open request %d: expected Retry-After header", i)
		}
		secs, err := strconv.Atoi(retryAfter)
		if err != nil || secs <= 0 {
			t.Fatalf("all-open request %d: expected positive integer Retry-After, got %q", i, retryAfter)
		}
		if !strings.Contains(rr.Body.String(), "circuit breaker is open") {
			t.Fatalf("all-open request %d: expected circuit breaker open message, got %q", i, rr.Body.String())
		}
	}
}

// TestCircuitBreaker_BackendResponseHeaderTimeoutTripsBreaker verifies that if an upstream backend hangs,
// ResponseHeaderTimeout aborts the request, increments failures, and trips the circuit breaker to Open.
func TestCircuitBreaker_BackendResponseHeaderTimeoutTripsBreaker(t *testing.T) {
	// Backend that sleeps 300ms, exceeding the 40ms transport timeout
	hangingBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"too_late"}`))
	}))
	defer hangingBackend.Close()

	u, err := url.Parse(hangingBackend.URL)
	if err != nil {
		t.Fatalf("failed to parse backend URL: %v", err)
	}

	cbThreshold := 3
	cbCooldown := 1 * time.Second
	cb := NewCircuitBreaker(cbThreshold, cbCooldown)

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 40 * time.Millisecond

	node := NewBackendNode(u, cb, transport)
	pool, err := NewBackendPool([]*BackendNode{node})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}

	limiter := &mockLimiterAllowed{}
	metrics := NewGatewayMetrics(prometheus.NewRegistry())
	handler := newGatewayHandler(testAPIKey, pool, limiter, metrics)

	// Send requests that will time out
	for i := 1; i <= cbThreshold; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		// 502 Bad Gateway returned by reverse proxy when transport times out
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("request %d: expected 502 Bad Gateway on timeout, got %d", i, rr.Code)
		}

		if cb.ConsecutiveFailures() != i && i < cbThreshold {
			t.Fatalf("request %d: expected %d consecutive failures, got %d", i, i, cb.ConsecutiveFailures())
		}
	}

	// Verify breaker has tripped to StateOpen
	if cb.State() != StateOpen {
		t.Fatalf("expected breaker to be StateOpen after %d timeouts, got %v", cbThreshold, cb.State())
	}

	// Subsequent request fast-fails with 503 Service Unavailable and Retry-After
	req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req.Header.Set("X-API-Key", testAPIKey)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable when breaker is open, got %d", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header on 503 open breaker response")
	}
}

// TestGateway_GracefulShutdownInFlight verifies that an in-flight request finishes cleanly
// during server shutdown, the server exits with ErrServerClosed, and new requests are rejected.
func TestGateway_GracefulShutdownInFlight(t *testing.T) {
	// Backend with simulated latency
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(120 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"in_flight_success"}`))
	}))
	defer backend.Close()

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("failed to parse backend URL: %v", err)
	}

	cb := NewCircuitBreaker(5, 10*time.Second)
	node := NewBackendNode(backendURL, cb, http.DefaultTransport)
	pool, err := NewBackendPool([]*BackendNode{node})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}

	limiter := &mockLimiterAllowed{}
	metrics := NewGatewayMetrics(prometheus.NewRegistry())
	handler := newGatewayHandler(testAPIKey, pool, limiter, metrics)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	server := &http.Server{
		Handler: handler,
	}

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- server.Serve(listener)
	}()

	addr := listener.Addr().String()

	type reqResult struct {
		statusCode int
		err        error
		body       string
	}
	resCh := make(chan reqResult, 1)

	// Launch in-flight request
	go func() {
		req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/api/students", nil)
		if err != nil {
			resCh <- reqResult{err: err}
			return
		}
		req.Header.Set("X-API-Key", testAPIKey)
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			resCh <- reqResult{err: err}
			return
		}
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)
		resCh <- reqResult{
			statusCode: resp.StatusCode,
			body:       string(bodyBytes),
		}
	}()

	// Allow request to enter the gateway and reach the backend
	time.Sleep(30 * time.Millisecond)

	// Trigger graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("server.Shutdown failed: %v", err)
	}

	// Verify in-flight request completed successfully with 200 OK
	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("in-flight request failed: %v", res.err)
		}
		if res.statusCode != http.StatusOK {
			t.Fatalf("expected in-flight status 200 OK, got %d", res.statusCode)
		}
		if !strings.Contains(res.body, "in_flight_success") {
			t.Fatalf("unexpected body: %q", res.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for in-flight request to complete during shutdown")
	}

	// Verify server exited with ErrServerClosed
	if err := <-serverErrCh; err != nil && err != http.ErrServerClosed {
		t.Fatalf("expected ErrServerClosed, got: %v", err)
	}

	// Verify new connection after shutdown fails
	newReq, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/api/students", nil)
	newReq.Header.Set("X-API-Key", testAPIKey)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	_, err = client.Do(newReq)
	if err == nil {
		t.Fatal("expected request after shutdown to fail, but it succeeded")
	}
}

// mockExceededLimiter simulates a rate limiter that rejects requests due to quota exhaustion.
type mockExceededLimiter struct{}

func (m *mockExceededLimiter) Allow(ctx context.Context, identity string) (RateLimitResult, error) {
	return RateLimitResult{
		Allowed:    false,
		Limit:      10,
		Remaining:  0,
		RetryAfter: 45,
	}, nil
}

// TestMetrics_ComprehensiveStatusAccounting verifies that all key status codes
// (200, 401, 429, 502, 503) are accurately reflected in Prometheus request metrics.
func TestMetrics_ComprehensiveStatusAccounting(t *testing.T) {
	// Healthy backend
	healthyBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer healthyBackend.Close()

	healthyURL, _ := url.Parse(healthyBackend.URL)
	cbHealthy := NewCircuitBreaker(5, 10*time.Second)
	nodeHealthy := NewBackendNode(healthyURL, cbHealthy, http.DefaultTransport)
	poolHealthy, _ := NewBackendPool([]*BackendNode{nodeHealthy})

	// Broken backend (unreachable port for 502)
	brokenURL, _ := url.Parse("http://127.0.0.1:59998")
	cbBroken := NewCircuitBreaker(10, 10*time.Second) // high threshold to not trip
	nodeBroken := NewBackendNode(brokenURL, cbBroken, http.DefaultTransport)
	poolBroken, _ := NewBackendPool([]*BackendNode{nodeBroken})

	reg := prometheus.NewRegistry()
	metrics := NewGatewayMetrics(reg)

	allowedLimiter := &mockLimiterAllowed{}
	exceededLimiter := &mockExceededLimiter{}
	failingLimiter := &mockFailingLimiter{}

	// Handler with healthy backend
	hHealthy := newGatewayHandler(testAPIKey, poolHealthy, allowedLimiter, metrics)
	// Handler with exceeded quota
	hExceeded := newGatewayHandler(testAPIKey, poolHealthy, exceededLimiter, metrics)
	// Handler with failing Redis
	hFailingRedis := newGatewayHandler(testAPIKey, poolHealthy, failingLimiter, metrics)
	// Handler with broken backend
	hBrokenBackend := newGatewayHandler(testAPIKey, poolBroken, allowedLimiter, metrics)

	// 1. Send 2x 200 OK
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		hHealthy.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rr.Code)
		}
	}

	// 2. Send 3x 401 Unauthorized
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", "invalid-key")
		rr := httptest.NewRecorder()
		hHealthy.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rr.Code)
		}
	}

	// 3. Send 4x 429 Too Many Requests
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		hExceeded.ServeHTTP(rr, req)
		if rr.Code != http.StatusTooManyRequests {
			t.Fatalf("expected 429, got %d", rr.Code)
		}
	}

	// 4. Send 2x 503 Service Unavailable (Redis error)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		hFailingRedis.ServeHTTP(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d", rr.Code)
		}
	}

	// 5. Send 1x 502 Bad Gateway (Backend transport error)
	{
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rr := httptest.NewRecorder()
		hBrokenBackend.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("expected 502, got %d", rr.Code)
		}
	}

	// Verify counts from Prometheus exposition format
	rec := httptest.NewRecorder()
	scrapeReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	hHealthy.ServeHTTP(rec, scrapeReq)

	metricsOutput := rec.Body.String()

	expectedCounts := map[string]string{
		`gateway_requests_total{code="200",method="GET"}`: "2",
		`gateway_requests_total{code="401",method="GET"}`: "3",
		`gateway_requests_total{code="429",method="GET"}`: "4",
		`gateway_requests_total{code="503",method="GET"}`: "2",
		`gateway_requests_total{code="502",method="GET"}`: "1",
	}

	for metricLine, expectedVal := range expectedCounts {
		found := false
		for _, line := range strings.Split(metricsOutput, "\n") {
			if strings.HasPrefix(line, metricLine) {
				found = true
				parts := strings.Fields(line)
				if len(parts) >= 2 && parts[1] != expectedVal {
					t.Errorf("for %s: expected %s, got %s", metricLine, expectedVal, parts[1])
				}
				break
			}
		}
		if !found {
			t.Errorf("expected metric line %s not found in output", metricLine)
		}
	}
}
