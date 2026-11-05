package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestBackendURLRejectsUnsupportedTargets(t *testing.T) {
	for _, raw := range []string{"ftp://host", "http://user:password@host", "https://host/#fragment"} {
		if _, err := parseBackendURLs(raw, ""); err == nil {
			t.Errorf("accepted invalid target %q", raw)
		}
	}
}

func TestBackendProxyHostAndCredentialIsolation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "" {
			t.Error("gateway credential leaked upstream")
		}
		if r.Host == "public.example" {
			t.Error("public host forwarded upstream")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	node := NewBackendNode(target, nil, nil)
	req := httptest.NewRequest("GET", "http://public.example/api/students", nil)
	req.Header.Set("X-API-Key", "private-key")
	res := httptest.NewRecorder()
	node.proxy.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("got %d", res.Code)
	}
}

func TestFlushCommitsStatus(t *testing.T) {
	res := httptest.NewRecorder()
	writer := newStatusResponseWriter(res)
	writer.Flush()
	writer.WriteHeader(http.StatusBadGateway)
	if writer.statusCode != http.StatusOK || res.Code != http.StatusOK {
		t.Fatal("flush did not commit 200")
	}
}

func TestEmptyConfiguredKeyFailsClosed(t *testing.T) {
	handler := newGatewayHandler("", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("forwarded unauthenticated request") }), nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest("GET", "/api/students", nil))
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", res.Code)
	}
}

func TestClientCancellationDoesNotTripBreaker(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	cb := NewCircuitBreaker(2, 5*time.Second)
	node := NewBackendNode(u, cb, http.DefaultTransport)

	// Dispatch 3 requests with immediate context cancellation
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // immediately canceled
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/api/students", nil)
		rr := httptest.NewRecorder()
		node.proxy.ServeHTTP(rr, req)
	}

	// Verify breaker is still Closed and consecutiveFailures is 0
	if cb.State() != StateClosed {
		t.Fatalf("expected StateClosed, got %s", cb.State())
	}
	if cb.ConsecutiveFailures() != 0 {
		t.Fatalf("expected 0 consecutive failures, got %d", cb.ConsecutiveFailures())
	}
}

func TestRateLimitFailureMode_FailClosedAndFailOpen(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	node := NewBackendNode(u, nil, nil)
	failingLimiter := &mockFailingLimiter{}
	reg := prometheus.NewRegistry()
	metrics := NewGatewayMetrics(reg)

	// 1. Fail-closed (default)
	handlerClosed := newGatewayHandlerWithOptions("test-key", node.proxy, failingLimiter, WithMetrics(metrics), WithRedisFailureMode("fail-closed"))
	req1 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req1.Header.Set("X-API-Key", "test-key")
	rr1 := httptest.NewRecorder()
	handlerClosed.ServeHTTP(rr1, req1)

	if rr1.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for fail-closed, got %d", rr1.Code)
	}

	// 2. Fail-open
	handlerOpen := newGatewayHandlerWithOptions("test-key", node.proxy, failingLimiter, WithMetrics(metrics), WithRedisFailureMode("fail-open"))
	req2 := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req2.Header.Set("X-API-Key", "test-key")
	rr2 := httptest.NewRecorder()
	handlerOpen.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200 for fail-open, got %d", rr2.Code)
	}
	if rr2.Header().Get("X-RateLimit-Degraded") != "true" {
		t.Errorf("expected X-RateLimit-Degraded: true header, got %q", rr2.Header().Get("X-RateLimit-Degraded"))
	}
}

type mockLimiterWithTTL struct{}

func (m *mockLimiterWithTTL) Allow(ctx context.Context, identity string) (RateLimitResult, error) {
	return RateLimitResult{
		Allowed:    true,
		Limit:      10,
		Remaining:  9,
		RetryAfter: 45,
	}, nil
}

func TestRateLimitResetHeader(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	node := NewBackendNode(u, nil, nil)
	limiter := &mockLimiterWithTTL{}
	handler := newGatewayHandler("test-key", node.proxy, limiter)

	req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	req.Header.Set("X-API-Key", "test-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rr.Code)
	}
	resetVal := rr.Header().Get("X-RateLimit-Reset")
	if resetVal == "" {
		t.Fatalf("expected X-RateLimit-Reset header to be set")
	}
	resetInt, err := strconv.ParseInt(resetVal, 10, 64)
	if err != nil || resetInt <= time.Now().Unix() {
		t.Fatalf("expected valid future reset epoch timestamp, got %s", resetVal)
	}
}

func TestMaxRequestBodyBytes(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	node := NewBackendNode(u, nil, nil)
	limiter := &mockLimiterAllowed{}
	// Limit to 10 bytes
	handler := newGatewayHandlerWithOptions("test-key", node.proxy, limiter, WithMaxBodyBytes(10))

	// Send 50 bytes
	largeBody := bytes.Repeat([]byte("a"), 50)
	req := httptest.NewRequest(http.MethodPost, "/api/students", bytes.NewReader(largeBody))
	req.Header.Set("X-API-Key", "test-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code == http.StatusOK {
		t.Fatalf("expected request exceeding max body bytes to fail, got %d", rr.Code)
	}
}

func TestActiveHealthChecker(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(false)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if healthy.Load() {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"healthy"}`))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	cb := NewCircuitBreaker(1, 10*time.Millisecond)
	node := NewBackendNode(u, cb, http.DefaultTransport)
	pool, _ := NewBackendPool([]*BackendNode{node})

	// Trip circuit breaker
	cb.afterRequest(false, errors.New("initial failure"))
	if cb.State() != StateOpen {
		t.Fatalf("expected StateOpen, got %s", cb.State())
	}

	// Backend becomes healthy
	healthy.Store(true)
	time.Sleep(15 * time.Millisecond) // wait past cooldown

	// Run active health check probe
	hc := NewHealthChecker(pool, 50*time.Millisecond, "/api/students", 500*time.Millisecond)
	hc.CheckNow()

	// Breaker should have been recovered to StateClosed
	if cb.State() != StateClosed {
		t.Fatalf("expected StateClosed after health check recovery, got %s", cb.State())
	}
	// Verify Start and Stop lifecycle and idempotency
	hc.Start()
	hc.Start()
	hc.Stop()
	hc.Stop()
}

func TestSecurityHeadersMiddleware(t *testing.T) {
	dummy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := securityHeadersMiddleware(corsMiddleware("*", dummy))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("expected X-Content-Type-Options: nosniff, got %q", got)
	}
	if got := rr.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("expected X-Frame-Options: DENY, got %q", got)
	}
	if got := rr.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "X-RateLimit-Reset") {
		t.Errorf("expected Access-Control-Expose-Headers to contain X-RateLimit-Reset, got %q", got)
	}
}
