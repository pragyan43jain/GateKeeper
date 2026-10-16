package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

// RateLimiter defines the interface for checking and consuming rate limits.
type RateLimiter interface {
	Allow(ctx context.Context, identity string) (RateLimitResult, error)
}

// RateLimitResult contains the outcome of a rate limit check.
type RateLimitResult struct {
	Allowed    bool
	Limit      int64
	Remaining  int64
	RetryAfter int64 // in seconds
}

// Lua script to atomically increment the counter and set TTL if not already set.
// Concurrency-safe: Redis evaluates Lua scripts atomically.
var rateLimitScript = redis.NewScript(`
local key = KEYS[1]
local limit = tonumber(ARGV[1])
local window = tonumber(ARGV[2])

local current = redis.call('INCR', key)
if current == 1 then
    redis.call('EXPIRE', key, window)
end

local ttl = redis.call('TTL', key)
if ttl < 0 then
    redis.call('EXPIRE', key, window)
    ttl = window
end

if current > limit then
    return {0, current, ttl}
else
    return {1, current, ttl}
end
`)

// RedisRateLimiter implements RateLimiter using Redis.
type RedisRateLimiter struct {
	client redis.UniversalClient
	prefix string
	limit  int64
	window time.Duration
}

// NewRedisRateLimiter creates a new RedisRateLimiter instance.
func NewRedisRateLimiter(client redis.UniversalClient, prefix string, limit int64, window time.Duration) *RedisRateLimiter {
	return &RedisRateLimiter{
		client: client,
		prefix: prefix,
		limit:  limit,
		window: window,
	}
}

// Allow atomically checks and increments the rate limit counter for the given identity hash.
func (r *RedisRateLimiter) Allow(ctx context.Context, identity string) (RateLimitResult, error) {
	key := r.prefix + identity
	windowSecs := int64(r.window.Seconds())
	if windowSecs <= 0 {
		windowSecs = 1
	}

	res, err := rateLimitScript.Run(ctx, r.client, []string{key}, r.limit, windowSecs).Int64Slice()
	if err != nil {
		return RateLimitResult{}, err
	}

	if len(res) < 3 {
		return RateLimitResult{}, fmt.Errorf("unexpected rate limit script response length: %d", len(res))
	}

	allowed := res[0] == 1
	current := res[1]
	ttl := res[2]

	if ttl < 1 {
		ttl = 1
	}

	remaining := r.limit - current
	if remaining < 0 {
		remaining = 0
	}

	return RateLimitResult{
		Allowed:    allowed,
		Limit:      r.limit,
		Remaining:  remaining,
		RetryAfter: ttl,
	}, nil
}

// hashAPIKey derives a stable SHA-256 hex string from the API key.
// The raw API key must never be used as a Redis key or written to logs.
func hashAPIKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])
}

// CircuitState represents the operational state of the circuit breaker.
type CircuitState int

const (
	StateClosed CircuitState = iota
	StateOpen
	StateHalfOpen
)

func (s CircuitState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreaker manages failure tracking and state transitions for upstream calls.
type CircuitBreaker struct {
	mu                  sync.Mutex
	state               CircuitState
	consecutiveFailures int
	failureThreshold    int
	cooldown            time.Duration
	openedAt            time.Time
	halfOpenInFlight    bool
	clock               func() time.Time
	name                string
	metrics             *GatewayMetrics
}

// NewCircuitBreaker creates a new CircuitBreaker with the specified threshold and cooldown.
// An optional clock function can be provided for deterministic testing.
func NewCircuitBreaker(threshold int, cooldown time.Duration, clock ...func() time.Time) *CircuitBreaker {
	if threshold <= 0 {
		threshold = 5
	}
	if cooldown <= 0 {
		cooldown = 10 * time.Second
	}
	var clk func() time.Time
	if len(clock) > 0 && clock[0] != nil {
		clk = clock[0]
	}
	return &CircuitBreaker{
		state:            StateClosed,
		failureThreshold: threshold,
		cooldown:         cooldown,
		clock:            clk,
	}
}

// SetName sets the backend identifier for metrics tracking and logging.
func (cb *CircuitBreaker) SetName(name string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.name = name
	if cb.metrics != nil && name != "" {
		if cb.metrics.backendCircuitBreakerState != nil {
			cb.metrics.backendCircuitBreakerState.WithLabelValues(name).Set(float64(cb.state))
		}
		if cb.metrics.backendCircuitBreakerTrips != nil {
			cb.metrics.backendCircuitBreakerTrips.WithLabelValues(name).Add(0)
		}
	}
}

// Name returns the backend identifier.
func (cb *CircuitBreaker) Name() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.name
}

// isAvailable returns true if the circuit breaker can accept a request:
// - Closed: available
// - Half-Open: available for probe if no probe is currently in flight
// - Open: available for probe if cooldown has elapsed
func (cb *CircuitBreaker) isAvailable() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	state := cb.checkStateLocked()
	if state == StateClosed {
		return true
	}
	if state == StateHalfOpen && !cb.halfOpenInFlight {
		return true
	}
	return false
}

// SetClock configures a custom clock function (used for deterministic testing).
func (cb *CircuitBreaker) SetClock(clk func() time.Time) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.clock = clk
}

// SetMetrics attaches GatewayMetrics to the circuit breaker for state and trip tracking.
func (cb *CircuitBreaker) SetMetrics(m *GatewayMetrics) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.metrics = m
	if m != nil {
		if m.circuitBreakerState != nil {
			m.circuitBreakerState.Set(float64(cb.state))
		}
		if cb.name != "" {
			if m.backendCircuitBreakerState != nil {
				m.backendCircuitBreakerState.WithLabelValues(cb.name).Set(float64(cb.state))
			}
			if m.backendCircuitBreakerTrips != nil {
				m.backendCircuitBreakerTrips.WithLabelValues(cb.name).Add(0)
			}
		}
	}
}

func (cb *CircuitBreaker) now() time.Time {
	if cb.clock != nil {
		return cb.clock()
	}
	return time.Now()
}

func (cb *CircuitBreaker) transitionToOpenLocked() {
	cb.state = StateOpen
	cb.openedAt = cb.now()
	cb.halfOpenInFlight = false
	if cb.metrics != nil {
		if cb.metrics.circuitBreakerState != nil {
			cb.metrics.circuitBreakerState.Set(float64(StateOpen))
		}
		if cb.metrics.circuitBreakerTrips != nil {
			cb.metrics.circuitBreakerTrips.Inc()
		}
		if cb.metrics.backendCircuitBreakerState != nil && cb.name != "" {
			cb.metrics.backendCircuitBreakerState.WithLabelValues(cb.name).Set(float64(StateOpen))
		}
		if cb.metrics.backendCircuitBreakerTrips != nil && cb.name != "" {
			cb.metrics.backendCircuitBreakerTrips.WithLabelValues(cb.name).Inc()
		}
	}
}

func (cb *CircuitBreaker) transitionToHalfOpenLocked() {
	cb.state = StateHalfOpen
	cb.halfOpenInFlight = false
	if cb.metrics != nil {
		if cb.metrics.circuitBreakerState != nil {
			cb.metrics.circuitBreakerState.Set(float64(StateHalfOpen))
		}
		if cb.metrics.backendCircuitBreakerState != nil && cb.name != "" {
			cb.metrics.backendCircuitBreakerState.WithLabelValues(cb.name).Set(float64(StateHalfOpen))
		}
	}
}

func (cb *CircuitBreaker) transitionToClosedLocked() {
	cb.state = StateClosed
	cb.consecutiveFailures = 0
	cb.halfOpenInFlight = false
	if cb.metrics != nil {
		if cb.metrics.circuitBreakerState != nil {
			cb.metrics.circuitBreakerState.Set(float64(StateClosed))
		}
		if cb.metrics.backendCircuitBreakerState != nil && cb.name != "" {
			cb.metrics.backendCircuitBreakerState.WithLabelValues(cb.name).Set(float64(StateClosed))
		}
	}
}

func (cb *CircuitBreaker) checkStateLocked() CircuitState {
	if cb.state == StateOpen && cb.now().Sub(cb.openedAt) >= cb.cooldown {
		cb.transitionToHalfOpenLocked()
	}
	return cb.state
}

// State returns the current circuit breaker state.
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.checkStateLocked()
}

// ConsecutiveFailures returns the current count of consecutive transport failures.
func (cb *CircuitBreaker) ConsecutiveFailures() int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.consecutiveFailures
}

// beforeRequest determines if a request should be permitted.
// Returns:
//
//	allowed: whether the request is allowed through
//	isProbe: whether this request is the single half-open trial probe
//	retryAfter: in seconds, if not allowed
func (cb *CircuitBreaker) beforeRequest() (allowed bool, isProbe bool, retryAfter int64) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	state := cb.checkStateLocked()

	switch state {
	case StateClosed:
		return true, false, 0

	case StateHalfOpen:
		if cb.halfOpenInFlight {
			// Another request is currently probing; fail fast
			return false, false, 1
		}
		// Claim the single trial probe
		cb.halfOpenInFlight = true
		return true, true, 0

	case StateOpen:
		remaining := cb.cooldown - cb.now().Sub(cb.openedAt)
		retrySecs := int64(math.Ceil(remaining.Seconds()))
		if retrySecs < 1 {
			retrySecs = 1
		}
		return false, false, retrySecs

	default:
		return true, false, 0
	}
}

// afterRequest records the outcome of a transport attempt.
func (cb *CircuitBreaker) afterRequest(isProbe bool, err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if isProbe {
		if err != nil {
			// Probe failed: reopen circuit and reset cooldown timer
			cb.transitionToOpenLocked()
		} else {
			// Probe succeeded: close circuit and clear failures
			cb.transitionToClosedLocked()
		}
		return
	}

	// Normal (non-probe) request
	if err != nil {
		if cb.state == StateClosed {
			cb.consecutiveFailures++
			if cb.consecutiveFailures >= cb.failureThreshold {
				cb.transitionToOpenLocked()
			}
		}
	} else {
		// Successful response resets consecutive failure counter if in closed state
		if cb.state == StateClosed {
			cb.consecutiveFailures = 0
		}
	}
}

// CircuitBreakerTransport wraps an http.RoundTripper with circuit breaking logic.
type CircuitBreakerTransport struct {
	cb   *CircuitBreaker
	base http.RoundTripper
}

// NewCircuitBreakerTransport creates an http.RoundTripper that guards the base transport with a circuit breaker.
func NewCircuitBreakerTransport(cb *CircuitBreaker, base http.RoundTripper) *CircuitBreakerTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &CircuitBreakerTransport{
		cb:   cb,
		base: base,
	}
}

// RoundTrip executes an HTTP round trip with circuit breaker protections.
func (t *CircuitBreakerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	allowed, isProbe, retryAfter := t.cb.beforeRequest()
	if !allowed {
		return t.openCircuitResponse(req, retryAfter), nil
	}

	var (
		resp *http.Response
		err  error
		done bool
	)
	defer func() {
		if !done {
			// In case of panic or abrupt termination, record failure
			t.cb.afterRequest(isProbe, errors.New("upstream transport terminated abnormally"))
		}
	}()

	resp, err = t.base.RoundTrip(req)
	done = true
	t.cb.afterRequest(isProbe, err)
	return resp, err
}

func (t *CircuitBreakerTransport) openCircuitResponse(req *http.Request, retryAfter int64) *http.Response {
	bodyMsg := fmt.Sprintf("Service Unavailable: circuit breaker is open. Retry after %d seconds\n", retryAfter)
	bodyBytes := []byte(bodyMsg)

	header := make(http.Header)
	header.Set("Content-Type", "text/plain; charset=utf-8")
	header.Set("Retry-After", strconv.FormatInt(retryAfter, 10))
	header.Set("Content-Length", strconv.Itoa(len(bodyBytes)))

	proto := "HTTP/1.1"
	protoMajor := 1
	protoMinor := 1
	if req != nil && req.Proto != "" {
		proto = req.Proto
		protoMajor = req.ProtoMajor
		protoMinor = req.ProtoMinor
	}

	return &http.Response{
		StatusCode:    http.StatusServiceUnavailable,
		Status:        "503 Service Unavailable",
		Proto:         proto,
		ProtoMajor:    protoMajor,
		ProtoMinor:    protoMinor,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(bodyBytes)),
		ContentLength: int64(len(bodyBytes)),
		Request:       req,
	}
}

// BackendNode encapsulates an upstream destination, its dedicated reverse proxy, and its circuit breaker.
type BackendNode struct {
	url   *url.URL
	name  string
	proxy *httputil.ReverseProxy
	cb    *CircuitBreaker
}

// NewBackendNode constructs a new BackendNode with its own ReverseProxy and CircuitBreaker.
func NewBackendNode(target *url.URL, cb *CircuitBreaker, transport http.RoundTripper) *BackendNode {
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = NewCircuitBreakerTransport(cb, transport)

	name := target.Host
	if name == "" {
		name = target.String()
	}
	if cb != nil {
		cb.SetName(name)
	}

	return &BackendNode{
		url:   target,
		name:  name,
		proxy: proxy,
		cb:    cb,
	}
}

// URL returns the backend target URL.
func (n *BackendNode) URL() *url.URL {
	return n.url
}

// Name returns the backend identifier used for logging and metrics.
func (n *BackendNode) Name() string {
	return n.name
}

// ReverseProxy returns the dedicated reverse proxy for this backend.
func (n *BackendNode) ReverseProxy() *httputil.ReverseProxy {
	return n.proxy
}

// CircuitBreaker returns the dedicated circuit breaker for this backend.
func (n *BackendNode) CircuitBreaker() *CircuitBreaker {
	return n.cb
}

// IsAvailable returns true if the backend's circuit breaker is in a state that permits requests.
func (n *BackendNode) IsAvailable() bool {
	if n.cb == nil {
		return true
	}
	return n.cb.isAvailable()
}

// ConsecutiveFailures returns the consecutive transport failure count of this backend.
func (n *BackendNode) ConsecutiveFailures() int {
	if n.cb == nil {
		return 0
	}
	return n.cb.ConsecutiveFailures()
}

// State returns the circuit breaker state for this backend.
func (n *BackendNode) State() CircuitState {
	if n.cb == nil {
		return StateClosed
	}
	return n.cb.State()
}

func (n *BackendNode) isAvailable() bool {
	return n.IsAvailable()
}

// BackendPool provides thread-safe round-robin load balancing across multiple backend nodes.
type BackendPool struct {
	nodes []*BackendNode
	idx   atomic.Uint64
}

// NewBackendPool initializes a BackendPool with the given slice of BackendNodes.
func NewBackendPool(nodes []*BackendNode) (*BackendPool, error) {
	if len(nodes) == 0 {
		return nil, errors.New("backend pool must contain at least one node")
	}
	return &BackendPool{
		nodes: nodes,
	}, nil
}

// Nodes returns all backend nodes in the pool.
func (p *BackendPool) Nodes() []*BackendNode {
	return p.nodes
}

// SelectNode selects a backend node using round-robin distribution, skipping open circuit breakers
// when healthy alternatives exist. If all backends are unavailable, it selects the scheduled node,
// which fast-fails with 503 Service Unavailable and Retry-After.
func (p *BackendPool) SelectNode() *BackendNode {
	total := len(p.nodes)
	if total == 1 {
		return p.nodes[0]
	}

	start := p.idx.Add(1) - 1

	// Scan for the first available (closed or trial-probe eligible) backend
	for i := 0; i < total; i++ {
		candidate := p.nodes[(start+uint64(i))%uint64(total)]
		if candidate.isAvailable() {
			return candidate
		}
	}

	// All backends are currently open / unavailable.
	// Route to scheduled round-robin node which will fast-fail with 503 and Retry-After.
	return p.nodes[start%uint64(total)]
}

// ServeHTTP delegates request handling to the selected backend node.
func (p *BackendPool) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	node := p.SelectNode()
	node.proxy.ServeHTTP(w, r)
}

// SetMetrics registers metrics with all circuit breakers in the pool.
func (p *BackendPool) SetMetrics(m *GatewayMetrics) {
	for _, node := range p.nodes {
		if node.cb != nil {
			node.cb.SetMetrics(m)
		}
	}
}

// UpdateMetrics synchronizes circuit breaker states across all nodes for metrics scraping.
func (p *BackendPool) UpdateMetrics() {
	allOpen := true
	anyHalfOpen := false
	for _, node := range p.nodes {
		if node.cb != nil {
			st := node.cb.State()
			if st != StateOpen {
				allOpen = false
			}
			if st == StateHalfOpen {
				anyHalfOpen = true
			}
		}
	}
	if len(p.nodes) > 0 {
		var firstCB *CircuitBreaker
		for _, node := range p.nodes {
			if node.cb != nil {
				firstCB = node.cb
				break
			}
		}
		if firstCB != nil && firstCB.metrics != nil && firstCB.metrics.circuitBreakerState != nil {
			if allOpen {
				firstCB.metrics.circuitBreakerState.Set(float64(StateOpen))
			} else if anyHalfOpen {
				firstCB.metrics.circuitBreakerState.Set(float64(StateHalfOpen))
			} else {
				firstCB.metrics.circuitBreakerState.Set(float64(StateClosed))
			}
		}
	}
}

// statusResponseWriter captures the response HTTP status code and delegates standard ResponseWriter methods.
type statusResponseWriter struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

func newStatusResponseWriter(w http.ResponseWriter) *statusResponseWriter {
	return &statusResponseWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
	}
}

func (rw *statusResponseWriter) WriteHeader(code int) {
	if !rw.wroteHeader {
		rw.statusCode = code
		rw.wroteHeader = true
		rw.ResponseWriter.WriteHeader(code)
	}
}

func (rw *statusResponseWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(b)
}

// Flush implements http.Flusher to ensure streaming / chunked proxy responses work properly.
func (rw *statusResponseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap supports http.ResponseController (Go 1.20+) for unwrapping underlying writers.
func (rw *statusResponseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// GatewayMetrics encapsulates Prometheus metrics for Gatekeeper.
type GatewayMetrics struct {
	registry                   *prometheus.Registry
	requestsTotal              *prometheus.CounterVec
	requestDuration            *prometheus.HistogramVec
	rateLimitRejections        prometheus.Counter
	circuitBreakerState        prometheus.Gauge
	circuitBreakerTrips        prometheus.Counter
	backendCircuitBreakerState *prometheus.GaugeVec
	backendCircuitBreakerTrips *prometheus.CounterVec
}

// NewGatewayMetrics initializes and registers Gatekeeper metrics with the given registry.
func NewGatewayMetrics(reg *prometheus.Registry) *GatewayMetrics {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}

	m := &GatewayMetrics{
		registry: reg,
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_requests_total",
				Help: "Total number of HTTP requests processed by Gatekeeper by status code and method.",
			},
			[]string{"code", "method"},
		),
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "gateway_request_duration_seconds",
				Help:    "Histogram of request latencies in seconds.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"code", "method"},
		),
		rateLimitRejections: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "gateway_rate_limit_rejections_total",
				Help: "Total number of requests rejected due to rate limiting (HTTP 429).",
			},
		),
		circuitBreakerState: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "gateway_circuit_breaker_state",
				Help: "Current state of the gateway circuit breaker (0=closed, 1=open, 2=half-open).",
			},
		),
		circuitBreakerTrips: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "gateway_circuit_breaker_trips_total",
				Help: "Total number of times the circuit breaker has transitioned into the open state.",
			},
		),
		backendCircuitBreakerState: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "gateway_backend_circuit_breaker_state",
				Help: "Current state of the circuit breaker for a specific backend (0=closed, 1=open, 2=half-open).",
			},
			[]string{"backend"},
		),
		backendCircuitBreakerTrips: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_backend_circuit_breaker_trips_total",
				Help: "Total number of times the circuit breaker for a specific backend has transitioned into open state.",
			},
			[]string{"backend"},
		),
	}

	m.circuitBreakerState.Set(float64(StateClosed))
	reg.MustRegister(
		m.requestsTotal,
		m.requestDuration,
		m.rateLimitRejections,
		m.circuitBreakerState,
		m.circuitBreakerTrips,
		m.backendCircuitBreakerState,
		m.backendCircuitBreakerTrips,
	)
	return m
}

// InstrumentHandler wraps an http.Handler with metrics tracking for application requests.
// Scrapes to /metrics are excluded from request counting and latency tracking.
func (m *GatewayMetrics) InstrumentHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Do not count the /metrics scrape itself in application request metrics
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		srw := newStatusResponseWriter(w)
		next.ServeHTTP(srw, r)
		duration := time.Since(start).Seconds()

		code := strconv.Itoa(srw.statusCode)
		m.requestsTotal.WithLabelValues(code, r.Method).Inc()
		m.requestDuration.WithLabelValues(code, r.Method).Observe(duration)

		if srw.statusCode == http.StatusTooManyRequests {
			m.rateLimitRejections.Inc()
		}
	})
}

// newGatewayHandler constructs the HTTP routing and middleware for Gatekeeper.
func newGatewayHandler(apiKey string, proxy http.Handler, limiter RateLimiter, metrics ...*GatewayMetrics) http.Handler {
	var m *GatewayMetrics
	if len(metrics) > 0 && metrics[0] != nil {
		m = metrics[0]
	} else {
		m = NewGatewayMetrics(prometheus.NewRegistry())
	}

	// Connect circuit breaker to metrics if proxy wraps a CircuitBreakerTransport or BackendPool
	if pool, ok := proxy.(*BackendPool); ok {
		pool.SetMetrics(m)
	} else if rp, ok := proxy.(*httputil.ReverseProxy); ok {
		if cbt, ok := rp.Transport.(*CircuitBreakerTransport); ok && cbt.cb != nil {
			cbt.cb.SetMetrics(m)
		}
	}

	mux := http.NewServeMux()

	// Protect API routes with API key authentication and rate limiting.
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		providedKey := r.Header.Get("X-API-Key")

		// 1. Authenticate: Reject missing or invalid keys before any rate limit check or forwarding.
		if subtle.ConstantTimeCompare(
			[]byte(providedKey),
			[]byte(apiKey),
		) != 1 {
			log.Printf("auth=unauthorized path=%s", r.URL.Path)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// 2. Derive rate-limit identity: SHA-256 hash (never store or log raw key).
		keyHash := hashAPIKey(providedKey)

		// 3. Apply rate limiting: check Redis counter atomically.
		res, err := limiter.Allow(r.Context(), keyHash)
		if err != nil {
			// Fail safely: do not silently bypass rate limiting when Redis is unavailable.
			log.Printf("rate_limit=error key_hash=%s path=%s error=%v", keyHash, r.URL.Path, err)
			http.Error(w, "Service Unavailable: rate limiter unavailable", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(res.Limit, 10))
		w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(res.Remaining, 10))

		if !res.Allowed {
			// Rate limit exceeded.
			log.Printf("rate_limit=exceeded key_hash=%s path=%s retry_after=%d", keyHash, r.URL.Path, res.RetryAfter)
			w.Header().Set("Retry-After", strconv.FormatInt(res.RetryAfter, 10))
			http.Error(w, fmt.Sprintf("Too Many Requests: rate limit exceeded. Retry after %d seconds", res.RetryAfter), http.StatusTooManyRequests)
			return
		}

		// Allowed.
		log.Printf("rate_limit=allowed key_hash=%s path=%s remaining=%d", keyHash, r.URL.Path, res.Remaining)

		// 4. Forward authenticated and rate-permitted request to backend.
		w.Header().Set("X-Gateway", "Gatekeeper")
		proxy.ServeHTTP(w, r)

		log.Printf(
			"method=%s path=%s duration=%s",
			r.Method,
			r.URL.Path,
			time.Since(start),
		)
	}))

	// Keep the health endpoint public and independent of API rate limits.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("Gatekeeper is healthy\n"))
	})

	// Dedicated metrics endpoint using Prometheus format. Unauthenticated and sensitive-data free.
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if pool, ok := proxy.(*BackendPool); ok {
			pool.UpdateMetrics()
		} else if rp, ok := proxy.(*httputil.ReverseProxy); ok {
			if cbt, ok := rp.Transport.(*CircuitBreakerTransport); ok && cbt.cb != nil {
				_ = cbt.cb.State()
			}
		}
		promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
	})

	return m.InstrumentHandler(mux)
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultVal
}

func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return defaultVal
}

func parseEnvIntPositive(key string, defaultVal int) (int, error) {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal, nil
	}
	i, err := strconv.Atoi(val)
	if err != nil {
		return 0, fmt.Errorf("invalid %s value %q: %w", key, val, err)
	}
	if i <= 0 {
		return 0, fmt.Errorf("%s must be greater than 0, got %d", key, i)
	}
	return i, nil
}

func parseEnvDurationPositive(key string, defaultVal time.Duration) (time.Duration, error) {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal, nil
	}
	d, err := time.ParseDuration(val)
	if err != nil {
		return 0, fmt.Errorf("invalid %s value %q: %w", key, val, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be greater than 0, got %v", key, d)
	}
	return d, nil
}

// parseBackendURLs parses comma-separated backend URLs, falling back to fallbackURL if urlsStr is empty.
func parseBackendURLs(urlsStr, fallbackURL string) ([]*url.URL, error) {
	targetStr := strings.TrimSpace(urlsStr)
	if targetStr == "" {
		targetStr = strings.TrimSpace(fallbackURL)
	}
	if targetStr == "" {
		return nil, errors.New("no backend URL configured")
	}

	parts := strings.Split(targetStr, ",")
	var urls []*url.URL
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		u, err := url.Parse(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid backend URL %q: %w", trimmed, err)
		}
		if u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("invalid backend URL %q: scheme and host are required", trimmed)
		}
		urls = append(urls, u)
	}

	if len(urls) == 0 {
		return nil, errors.New("no valid backend URLs provided")
	}
	return urls, nil
}

func main() {
	// Read the API key from the environment.
	apiKey := os.Getenv("GATEKEEPER_API_KEY")
	if apiKey == "" {
		log.Fatal("GATEKEEPER_API_KEY must be set")
	}

	// Configure backend destinations (supports comma-separated BACKEND_URLS, falls back to BACKEND_URL).
	backendURLs, err := parseBackendURLs(os.Getenv("BACKEND_URLS"), getEnv("BACKEND_URL", "http://localhost:8081"))
	if err != nil {
		log.Fatalf("backend configuration error: %v", err)
	}

	// Circuit breaker configuration.
	cbThreshold, err := parseEnvIntPositive("CIRCUIT_BREAKER_FAILURE_THRESHOLD", 5)
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	cbCooldown, err := parseEnvDurationPositive("CIRCUIT_BREAKER_COOLDOWN", 10*time.Second)
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	nodes := make([]*BackendNode, 0, len(backendURLs))
	for _, u := range backendURLs {
		cb := NewCircuitBreaker(cbThreshold, cbCooldown)
		node := NewBackendNode(u, cb, http.DefaultTransport)
		nodes = append(nodes, node)
	}

	pool, err := NewBackendPool(nodes)
	if err != nil {
		log.Fatalf("failed to initialize backend pool: %v", err)
	}

	// Redis connection settings.
	redisAddr := getEnv("REDIS_ADDR", "localhost:6379")
	redisPassword := os.Getenv("REDIS_PASSWORD")
	redisDB := getEnvInt("REDIS_DB", 0)

	rdb := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: redisPassword,
		DB:       redisDB,
	})
	defer rdb.Close()

	// Rate limiter configuration: 10 requests per 60 seconds by default.
	maxRequests := int64(getEnvInt("RATE_LIMIT_MAX_REQUESTS", 10))
	window := getEnvDuration("RATE_LIMIT_WINDOW", 60*time.Second)
	keyPrefix := getEnv("REDIS_KEY_PREFIX", "gatekeeper:rate_limit:")

	limiter := NewRedisRateLimiter(rdb, keyPrefix, maxRequests, window)
	metrics := NewGatewayMetrics(prometheus.NewRegistry())
	pool.SetMetrics(metrics)

	handler := newGatewayHandler(apiKey, pool, limiter, metrics)

	port := getEnv("PORT", "8080")

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	var backendStrs []string
	for _, u := range backendURLs {
		backendStrs = append(backendStrs, u.String())
	}
	log.Printf("Gatekeeper running at http://localhost:%s (backends: %v, redis: %s, cb_threshold: %d, cb_cooldown: %s)", port, backendStrs, redisAddr, cbThreshold, cbCooldown)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case err := <-serverErr:
		if err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}

	case sig := <-sigCh:
		log.Printf("Received %s; shutting down gracefully", sig)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			log.Printf("Graceful shutdown failed: %v; forcing close", err)
			if closeErr := server.Close(); closeErr != nil {
				log.Printf("Forced close failed: %v", closeErr)
			}
		}

		if err := <-serverErr; err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error during shutdown: %v", err)
		}
	}

}
