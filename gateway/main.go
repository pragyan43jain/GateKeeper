package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math"
	mathrand "math/rand"
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

type PolicyRateLimiter interface {
	AllowWithPolicy(ctx context.Context, identity string, limit int64, window time.Duration) (RateLimitResult, error)
}

// RateLimitResult contains the outcome of a rate limit check.
type RateLimitResult struct {
	Allowed    bool
	Limit      int64
	Remaining  int64
	RetryAfter int64 // in seconds
}

// Lua script maintains an atomic sliding log. Redis server time keeps all
// gateway instances on a shared clock and denied requests do not extend the window.
var rateLimitScript = redis.NewScript(`
local key = KEYS[1]
local limit = tonumber(ARGV[1])
local windowMs = tonumber(ARGV[2])
local member = ARGV[3]
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
redis.call('ZREMRANGEBYSCORE', key, '-inf', now - windowMs)
local current = redis.call('ZCARD', key)
local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
local retry = 1
if #oldest > 0 then retry = math.max(1, math.ceil((tonumber(oldest[2]) + windowMs - now) / 1000)) end
if current >= limit then return {0, current, retry} end
redis.call('ZADD', key, now, member)
redis.call('PEXPIRE', key, windowMs + 1000)
current = current + 1
oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
retry = math.max(1, math.ceil((tonumber(oldest[2]) + windowMs - now) / 1000))
return {1, current, retry}
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
	return r.AllowWithPolicy(ctx, identity, r.limit, r.window)
}

func (r *RedisRateLimiter) AllowWithPolicy(ctx context.Context, identity string, limit int64, window time.Duration) (RateLimitResult, error) {
	key := r.prefix + identity
	if limit <= 0 {
		return RateLimitResult{}, errors.New("rate limit must be greater than zero")
	}
	windowMs := window.Milliseconds()
	if windowMs <= 0 {
		windowMs = 1
	}
	if windowMs > int64((24 * time.Hour).Milliseconds()) {
		return RateLimitResult{}, errors.New("rate limit window must not exceed 24 hours")
	}
	var requestID [16]byte
	if _, err := rand.Read(requestID[:]); err != nil {
		return RateLimitResult{}, fmt.Errorf("generate rate limit event identifier: %w", err)
	}
	res, err := rateLimitScript.Run(ctx, r.client, []string{key}, limit, windowMs, hex.EncodeToString(requestID[:])).Int64Slice()
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

	remaining := limit - current
	if remaining < 0 {
		remaining = 0
	}

	return RateLimitResult{
		Allowed:    allowed,
		Limit:      limit,
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
func (cb *CircuitBreaker) afterRequest(isProbe bool, err error, clientCanceled ...bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if len(clientCanceled) > 0 && clientCanceled[0] {
		// Client aborted the request; do not penalize upstream backend or trip breaker.
		if isProbe {
			cb.halfOpenInFlight = false
		}
		return
	}

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

// OnHealthCheckSuccess can be called by active health checks when a backend probe succeeds.
// If the circuit breaker is in StateHalfOpen or StateOpen (after cooldown has passed),
// it transitions the circuit to StateClosed, immediately restoring traffic.
func (cb *CircuitBreaker) OnHealthCheckSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	state := cb.checkStateLocked()
	if state == StateHalfOpen || (state == StateOpen && cb.now().Sub(cb.openedAt) >= cb.cooldown) {
		cb.transitionToClosedLocked()
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
			clientCanceled := req != nil && errors.Is(req.Context().Err(), context.Canceled)
			t.cb.afterRequest(isProbe, errors.New("upstream transport terminated abnormally"), clientCanceled)
		}
	}()

	resp, err = t.base.RoundTrip(req)
	done = true
	clientCanceled := (req != nil && errors.Is(req.Context().Err(), context.Canceled)) || errors.Is(err, context.Canceled)
	t.cb.afterRequest(isProbe, err, clientCanceled)
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

// BackendHealthState denotes the active health state of an upstream node.
type BackendHealthState string

const (
	BackendHealthHealthy    BackendHealthState = "healthy"
	BackendHealthUnhealthy  BackendHealthState = "unhealthy"
	BackendHealthRecovering BackendHealthState = "recovering"
)

// BackendNode encapsulates an upstream destination, its dedicated reverse proxy, its circuit breaker,
// and its active health management state.
type BackendNode struct {
	url            *url.URL
	name           string
	configID       string
	healthPath     string
	proxy          *httputil.ReverseProxy
	cb             *CircuitBreaker
	weight         atomic.Int64
	activeRequests atomic.Int64
	maintenance    atomic.Bool

	healthMu             sync.RWMutex
	healthState          BackendHealthState
	consecutiveFailures  int
	consecutiveSuccesses int
	lastCheckTime        time.Time
	lastError            string
}

// NewBackendNode constructs a new BackendNode with its own ReverseProxy, CircuitBreaker, and health state.
func NewBackendNode(target *url.URL, cb *CircuitBreaker, transport http.RoundTripper) *BackendNode {
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	if cb != nil {
		proxy.Transport = NewCircuitBreakerTransport(cb, transport)
	}
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
		req.Header.Del("X-API-Key")
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.Request != nil {
			if requestID := resp.Request.Header.Get("X-Request-ID"); requestID != "" {
				// Keep the client-visible ID aligned with the gateway's request record,
				// even if an upstream returns its own X-Request-ID header.
				resp.Header.Set("X-Request-ID", requestID)
			}
		}
		return nil
	}

	name := target.Host
	if name == "" {
		name = target.String()
	}
	if cb != nil {
		cb.SetName(name)
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
		if req != nil && (errors.Is(req.Context().Err(), context.Canceled) || errors.Is(err, context.Canceled)) {
			// Client disconnected; do not attempt writing to closed connection
			return
		}
		log.Printf("http: proxy error: %v", err)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Gateway", "Gatekeeper")
		w.Header().Set("X-Backend-Id", name)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("Bad Gateway: upstream backend unreachable or timed out\n"))
	}

	node := &BackendNode{
		url:         target,
		name:        name,
		proxy:       proxy,
		cb:          cb,
		healthState: BackendHealthHealthy,
	}
	node.weight.Store(1)
	return node
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

func (n *BackendNode) SetWeight(weight int64) error {
	if weight < 1 || weight > 1000 {
		return errors.New("backend weight must be between 1 and 1000")
	}
	n.weight.Store(weight)
	return nil
}

func (n *BackendNode) Weight() int64 {
	weight := n.weight.Load()
	if weight < 1 {
		return 1
	}
	return weight
}

func (n *BackendNode) ActiveRequests() int64 {
	return n.activeRequests.Load()
}

// HealthState returns the current active health state of this backend.
func (n *BackendNode) HealthState() BackendHealthState {
	n.healthMu.RLock()
	defer n.healthMu.RUnlock()
	if n.healthState == "" {
		return BackendHealthHealthy
	}
	return n.healthState
}

// HealthInfo returns a snapshot of the backend's active health metrics.
func (n *BackendNode) HealthInfo() (state BackendHealthState, consecutiveFailures int, consecutiveSuccesses int, lastCheck time.Time, lastError string) {
	n.healthMu.RLock()
	defer n.healthMu.RUnlock()
	st := n.healthState
	if st == "" {
		st = BackendHealthHealthy
	}
	return st, n.consecutiveFailures, n.consecutiveSuccesses, n.lastCheckTime, n.lastError
}

// RecordHealthSuccess updates backend health state after a successful health check probe.
func (n *BackendNode) RecordHealthSuccess(unhealthyThreshold, recoveryThreshold int) {
	n.healthMu.Lock()
	defer n.healthMu.Unlock()

	n.lastCheckTime = time.Now()
	n.consecutiveFailures = 0
	n.consecutiveSuccesses++
	n.lastError = ""

	if n.healthState == BackendHealthUnhealthy {
		// Begin recovering
		n.healthState = BackendHealthRecovering
	}

	if n.healthState == BackendHealthRecovering {
		if n.consecutiveSuccesses >= recoveryThreshold {
			n.healthState = BackendHealthHealthy
			if n.cb != nil {
				n.cb.OnHealthCheckSuccess()
			}
		}
	} else {
		// Already healthy
		n.healthState = BackendHealthHealthy
		if n.cb != nil {
			n.cb.OnHealthCheckSuccess()
		}
	}
}

// RecordHealthFailure updates backend health state after a failed health check probe.
func (n *BackendNode) RecordHealthFailure(err error, unhealthyThreshold int) {
	n.healthMu.Lock()
	defer n.healthMu.Unlock()

	n.lastCheckTime = time.Now()
	n.consecutiveFailures++
	n.consecutiveSuccesses = 0
	if err != nil {
		n.lastError = err.Error()
	}

	if n.consecutiveFailures >= unhealthyThreshold {
		n.healthState = BackendHealthUnhealthy
	}
}

// IsAvailable returns true if the backend is healthy and its circuit breaker permits requests.
// Unhealthy and Recovering backends are excluded from standard traffic distribution until fully healthy.
func (n *BackendNode) IsAvailable() bool {
	n.healthMu.RLock()
	hState := n.healthState
	n.healthMu.RUnlock()

	if hState == BackendHealthUnhealthy || hState == BackendHealthRecovering {
		return false
	}

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

// ConsecutiveHealthFailures returns the consecutive active health check probe failure count.
func (n *BackendNode) ConsecutiveHealthFailures() int {
	n.healthMu.RLock()
	defer n.healthMu.RUnlock()
	return n.consecutiveFailures
}

// ConsecutiveHealthSuccesses returns the consecutive active health check probe success count.
func (n *BackendNode) ConsecutiveHealthSuccesses() int {
	n.healthMu.RLock()
	defer n.healthMu.RUnlock()
	return n.consecutiveSuccesses
}

// State returns the circuit breaker state for this backend.
func (n *BackendNode) State() CircuitState {
	if n.cb == nil {
		return StateClosed
	}
	return n.cb.State()
}

func (n *BackendNode) isAvailable() bool {
	return !n.maintenance.Load() && n.IsAvailable()
}

// BackendPool provides health-aware load balancing across multiple backend nodes.
type LoadBalancingStrategy string

const (
	StrategyRoundRobin         LoadBalancingStrategy = "round_robin"
	StrategyWeightedRoundRobin LoadBalancingStrategy = "weighted_round_robin"
	StrategyLeastConnections   LoadBalancingStrategy = "least_connections"
	StrategyRandom             LoadBalancingStrategy = "random"
)

type BackendPool struct {
	nodes         atomic.Pointer[backendNodeSet]
	idx           atomic.Uint64
	configureMu   sync.Mutex
	config        atomic.Pointer[loadBalanceConfig]
	smoothMu      sync.Mutex
	smoothWeights map[*BackendNode]int64
	baseTransport http.RoundTripper
	cbThreshold   int
	cbCooldown    time.Duration
	metrics       *GatewayMetrics
}

type backendNodeSet struct {
	nodes []*BackendNode
}

type loadBalanceConfig struct {
	strategy LoadBalancingStrategy
	weights  map[*BackendNode]int64
}

// NewBackendPool initializes a BackendPool with the given slice of BackendNodes.
func NewBackendPool(nodes []*BackendNode) (*BackendPool, error) {
	if len(nodes) == 0 {
		return nil, errors.New("backend pool must contain at least one node")
	}
	for _, node := range nodes {
		if node == nil || node.URL() == nil {
			return nil, errors.New("backend pool contains a nil node or URL")
		}
	}
	pool := &BackendPool{
		smoothWeights: make(map[*BackendNode]int64),
		cbThreshold:   5,
		cbCooldown:    10 * time.Second,
		baseTransport: http.DefaultTransport,
	}
	pool.nodes.Store(&backendNodeSet{nodes: append([]*BackendNode(nil), nodes...)})
	if len(nodes) > 0 && nodes[0].cb != nil {
		pool.cbThreshold = nodes[0].cb.failureThreshold
		pool.cbCooldown = nodes[0].cb.cooldown
	}
	if len(nodes) > 0 && nodes[0].proxy != nil && nodes[0].proxy.Transport != nil {
		pool.baseTransport = nodes[0].proxy.Transport
		if wrapped, ok := pool.baseTransport.(*CircuitBreakerTransport); ok {
			pool.baseTransport = wrapped.base
		}
	}
	weights := make(map[string]int64, len(nodes))
	for _, node := range nodes {
		weights[node.Name()] = node.Weight()
	}
	if err := pool.Configure(StrategyRoundRobin, weights); err != nil {
		return nil, err
	}
	return pool, nil
}

func (p *BackendPool) SetStrategy(strategy LoadBalancingStrategy) error {
	return p.Configure(strategy, nil)
}

func (p *BackendPool) Strategy() LoadBalancingStrategy {
	config := p.config.Load()
	if config == nil {
		return StrategyRoundRobin
	}
	return config.strategy
}

func (p *BackendPool) Weight(node *BackendNode) int64 {
	if node == nil {
		return 1
	}
	if config := p.config.Load(); config != nil {
		if weight, ok := config.weights[node]; ok {
			return weight
		}
	}
	return node.Weight()
}

// Configure validates and atomically publishes strategy and per-node weights.
func (p *BackendPool) Configure(strategy LoadBalancingStrategy, weights map[string]int64) error {
	p.configureMu.Lock()
	defer p.configureMu.Unlock()
	nodes := p.Nodes()
	if err := validatePoolConfiguration(nodes, p.config.Load(), strategy, weights); err != nil {
		return err
	}
	next := &loadBalanceConfig{strategy: strategy, weights: make(map[*BackendNode]int64, len(nodes))}
	for _, node := range nodes {
		weight := p.Weight(node)
		if configured, ok := weights[node.Name()]; ok {
			weight = configured
		}
		next.weights[node] = weight
	}
	for node, weight := range next.weights {
		node.weight.Store(weight)
	}
	p.config.Store(next)
	p.smoothMu.Lock()
	p.smoothWeights = make(map[*BackendNode]int64, len(next.weights))
	p.smoothMu.Unlock()
	return nil
}

func (p *BackendPool) ValidateConfiguration(strategy LoadBalancingStrategy, weights map[string]int64) error {
	return validatePoolConfiguration(p.Nodes(), p.config.Load(), strategy, weights)
}

func validatePoolConfiguration(nodes []*BackendNode, current *loadBalanceConfig, strategy LoadBalancingStrategy, weights map[string]int64) error {
	switch strategy {
	case StrategyRoundRobin, StrategyWeightedRoundRobin, StrategyLeastConnections, StrategyRandom:
	default:
		return fmt.Errorf("unsupported load balancing strategy %q", strategy)
	}
	for _, node := range nodes {
		weight := node.Weight()
		if current != nil {
			if configured, ok := current.weights[node]; ok {
				weight = configured
			}
		}
		if configured, ok := weights[node.Name()]; ok {
			weight = configured
		}
		if weight < 1 || weight > 1000 {
			return fmt.Errorf("invalid weight for backend %s: weight must be between 1 and 1000", node.Name())
		}
	}
	for name := range weights {
		found := false
		for _, node := range nodes {
			if node.Name() == name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("weight configured for unknown backend %q", name)
		}
	}
	return nil
}

// Nodes returns all backend nodes in the pool.
func (p *BackendPool) Nodes() []*BackendNode {
	set := p.nodes.Load()
	if set == nil {
		return nil
	}
	return append([]*BackendNode(nil), set.nodes...)
}

func (p *BackendPool) NodeByConfigID(id string) *BackendNode {
	if id == "" {
		return nil
	}
	for _, node := range p.Nodes() {
		if node.configID == id {
			return node
		}
	}
	return nil
}

// NewNode creates a backend node with this pool's transport and breaker policy.
func (p *BackendPool) NewNode(target *url.URL) *BackendNode {
	return NewBackendNode(target, NewCircuitBreaker(p.cbThreshold, p.cbCooldown), p.baseTransport)
}

// ReplaceNodes atomically swaps the active backend membership while preserving
// configured strategy and weights for nodes with matching names.
func (p *BackendPool) ReplaceNodes(nodes []*BackendNode) error {
	if len(nodes) == 0 {
		return errors.New("at least one enabled backend is required")
	}
	if len(nodes) > 50 {
		return errors.New("backend pool cannot contain more than 50 nodes")
	}
	p.configureMu.Lock()
	defer p.configureMu.Unlock()
	seen := make(map[string]struct{}, len(nodes))
	previous := p.config.Load()
	nextConfig := &loadBalanceConfig{strategy: StrategyRoundRobin, weights: make(map[*BackendNode]int64, len(nodes))}
	if previous != nil {
		nextConfig.strategy = previous.strategy
	}
	for _, node := range nodes {
		if node == nil || node.URL() == nil {
			return errors.New("backend pool contains a nil node or URL")
		}
		if _, exists := seen[node.Name()]; exists {
			return fmt.Errorf("duplicate backend host %q", node.Name())
		}
		seen[node.Name()] = struct{}{}
		weight := node.Weight()
		if weight < 1 || weight > 1000 {
			return fmt.Errorf("invalid weight for backend %s", node.Name())
		}
		if p.metrics != nil && node.cb != nil {
			node.cb.SetMetrics(p.metrics)
		}
		node.weight.Store(weight)
		nextConfig.weights[node] = weight
	}
	p.config.Store(nextConfig)
	p.nodes.Store(&backendNodeSet{nodes: append([]*BackendNode(nil), nodes...)})
	p.smoothMu.Lock()
	p.smoothWeights = make(map[*BackendNode]int64, len(nodes))
	p.smoothMu.Unlock()
	return nil
}

// SelectNode applies the configured strategy among healthy backends. It selects a
// configured node when all are unavailable so the breaker can return its controlled
// 503 response and Retry-After value.
func (p *BackendPool) SelectNode() *BackendNode {
	nodes := p.Nodes()
	total := len(nodes)
	if total == 1 {
		return nodes[0]
	}

	start := p.idx.Add(1) - 1

	// Scan for the first available (healthy and non-tripped) backend
	available := make([]*BackendNode, 0, total)
	for i := 0; i < total; i++ {
		candidate := nodes[(start+uint64(i))%uint64(total)]
		if candidate.isAvailable() {
			available = append(available, candidate)
		}
	}
	if len(available) > 0 {
		return p.selectAvailable(available, start)
	}

	// All backends are currently open or unhealthy; select one for a controlled fast-fail.
	return nodes[start%uint64(total)]
}

func (p *BackendPool) selectAvailable(nodes []*BackendNode, start uint64) *BackendNode {
	config := p.config.Load()
	strategy := StrategyRoundRobin
	if config != nil {
		strategy = config.strategy
	}
	switch strategy {
	case StrategyWeightedRoundRobin:
		p.smoothMu.Lock()
		defer p.smoothMu.Unlock()
		var totalWeight int64
		var selected *BackendNode
		var maxCurrent int64
		for _, node := range nodes {
			weight := config.weights[node]
			totalWeight += weight
			p.smoothWeights[node] += weight
			if selected == nil || p.smoothWeights[node] > maxCurrent {
				selected = node
				maxCurrent = p.smoothWeights[node]
			}
		}
		p.smoothWeights[selected] -= totalWeight
		return selected
	case StrategyLeastConnections:
		selected := nodes[0]
		minActive := selected.ActiveRequests()
		for _, node := range nodes[1:] {
			if active := node.ActiveRequests(); active < minActive {
				selected = node
				minActive = active
			}
		}
		return selected
	case StrategyRandom:
		return nodes[mathrand.Intn(len(nodes))]
	default:
		return nodes[0]
	}
}

// ServeHTTP delegates request handling to the selected backend node.
func (p *BackendPool) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	node := p.SelectNode()
	if !node.isAvailable() {
		// Health and maintenance exclusions are strict. If a node becomes excluded
		// between selection and dispatch, choose a healthy in-service peer or fail.
		eligible := make([]*BackendNode, 0)
		allUnhealthy := true
		for _, candidate := range p.Nodes() {
			if candidate.HealthState() == BackendHealthHealthy {
				allUnhealthy = false
			}
			if !candidate.maintenance.Load() && candidate.HealthState() == BackendHealthHealthy {
				eligible = append(eligible, candidate)
			}
		}
		if len(eligible) == 0 {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("X-Gateway", "Gatekeeper")
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusServiceUnavailable)
			message := "Service Unavailable: no healthy backend is in service\n"
			if allUnhealthy {
				message = "Service Unavailable: all backends are unhealthy\n"
			}
			_, _ = w.Write([]byte(message))
			return
		}
		node = eligible[0]
	}
	node.activeRequests.Add(1)
	defer node.activeRequests.Add(-1)
	node.proxy.ServeHTTP(w, r)
}

// ServeTarget forwards a request to the backend explicitly selected by a route.
// It does not fall back to another backend because that would violate the route.
func (p *BackendPool) ServeTarget(w http.ResponseWriter, r *http.Request, node *BackendNode) {
	if node == nil || node.maintenance.Load() || node.HealthState() != BackendHealthHealthy {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Gateway", "Gatekeeper")
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("Service Unavailable: route backend is unhealthy or out of service\n"))
		return
	}
	node.activeRequests.Add(1)
	defer node.activeRequests.Add(-1)
	node.proxy.ServeHTTP(w, r)
}

// SetMetrics registers metrics with all circuit breakers in the pool.
func (p *BackendPool) SetMetrics(m *GatewayMetrics) {
	p.configureMu.Lock()
	defer p.configureMu.Unlock()
	p.metrics = m
	for _, node := range p.Nodes() {
		if node.cb != nil {
			node.cb.SetMetrics(m)
		}
	}
}

// UpdateMetrics synchronizes circuit breaker states across all nodes for metrics scraping.
func (p *BackendPool) UpdateMetrics() {
	nodes := p.Nodes()
	allOpen := true
	anyHalfOpen := false
	for _, node := range nodes {
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
	if len(nodes) > 0 {
		var firstCB *CircuitBreaker
		for _, node := range nodes {
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

// HealthChecker periodically probes backend nodes to detect health and accelerate recovery.
type HealthChecker struct {
	pool               *BackendPool
	interval           time.Duration
	path               string
	timeout            time.Duration
	unhealthyThreshold int
	recoveryThreshold  int
	metrics            *GatewayMetrics
	client             *http.Client
	stopCh             chan struct{}
	doneCh             chan struct{}
	startOnce          sync.Once
	stopOnce           sync.Once
	started            atomic.Bool
}

// NewHealthChecker initializes a HealthChecker with configurable intervals and failure/recovery thresholds.
func NewHealthChecker(pool *BackendPool, interval time.Duration, path string, timeout time.Duration, thresholds ...int) *HealthChecker {
	if interval <= 0 || pool == nil {
		return nil
	}
	if path == "" {
		path = "/health"
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	unhealthyThresh := 3
	recoveryThresh := 2
	if len(thresholds) > 0 && thresholds[0] > 0 {
		unhealthyThresh = thresholds[0]
	}
	if len(thresholds) > 1 && thresholds[1] > 0 {
		recoveryThresh = thresholds[1]
	}

	return &HealthChecker{
		pool:               pool,
		interval:           interval,
		path:               path,
		timeout:            timeout,
		unhealthyThreshold: unhealthyThresh,
		recoveryThreshold:  recoveryThresh,
		client: &http.Client{
			Timeout: timeout,
		},
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// SetMetrics attaches gateway metrics for health check telemetry.
func (hc *HealthChecker) SetMetrics(m *GatewayMetrics) {
	if hc == nil {
		return
	}
	hc.metrics = m
}

// Start launches the background health check loop in a separate goroutine.
func (hc *HealthChecker) Start() {
	if hc == nil {
		return
	}
	hc.startOnce.Do(func() {
		hc.started.Store(true)
		go hc.run()
	})
}

// Stop gracefully shuts down the background health check loop.
func (hc *HealthChecker) Stop() {
	if hc == nil {
		return
	}
	hc.stopOnce.Do(func() {
		close(hc.stopCh)
		if hc.started.Load() {
			<-hc.doneCh
		}
	})
}

// CheckNow performs an immediate health check probe across all backend nodes.
func (hc *HealthChecker) CheckNow() {
	if hc == nil || hc.pool == nil {
		return
	}
	for _, node := range hc.pool.Nodes() {
		hc.probeNode(node)
	}
}

func (hc *HealthChecker) probeNode(node *BackendNode) {
	if node == nil || node.url == nil {
		return
	}
	healthPath := node.healthPath
	if healthPath == "" {
		healthPath = hc.path
	}
	probeURL := strings.TrimRight(node.url.String(), "/") + healthPath
	req, err := http.NewRequest(http.MethodGet, probeURL, nil)
	if err != nil {
		node.RecordHealthFailure(err, hc.unhealthyThreshold)
		hc.updateNodeMetrics(node, false)
		return
	}

	resp, err := hc.client.Do(req)
	if err != nil {
		node.RecordHealthFailure(err, hc.unhealthyThreshold)
		hc.updateNodeMetrics(node, false)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 400 {
		node.RecordHealthSuccess(hc.unhealthyThreshold, hc.recoveryThreshold)
		hc.updateNodeMetrics(node, true)
	} else {
		node.RecordHealthFailure(fmt.Errorf("HTTP %d", resp.StatusCode), hc.unhealthyThreshold)
		hc.updateNodeMetrics(node, false)
	}
}

func (hc *HealthChecker) updateNodeMetrics(node *BackendNode, success bool) {
	if hc.metrics == nil || node == nil {
		return
	}
	st := node.HealthState()
	val := 0.0 // healthy
	switch st {
	case BackendHealthUnhealthy:
		val = 1.0
	case BackendHealthRecovering:
		val = 2.0
	}
	if hc.metrics.backendHealthStatus != nil {
		hc.metrics.backendHealthStatus.WithLabelValues(node.Name()).Set(val)
	}
	if success {
		if hc.metrics.backendHealthSuccesses != nil {
			hc.metrics.backendHealthSuccesses.WithLabelValues(node.Name()).Inc()
		}
	} else {
		if hc.metrics.backendHealthFailures != nil {
			hc.metrics.backendHealthFailures.WithLabelValues(node.Name()).Inc()
		}
	}
}

func (hc *HealthChecker) run() {
	defer close(hc.doneCh)
	ticker := time.NewTicker(hc.interval)
	defer ticker.Stop()

	for {
		select {
		case <-hc.stopCh:
			return
		case <-ticker.C:
			hc.CheckNow()
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
	if code >= 100 && code < 200 {
		rw.ResponseWriter.WriteHeader(code)
		return
	}
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
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
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
	rateLimitErrors            prometheus.Counter
	rateLimitDegraded          prometheus.Counter
	circuitBreakerState        prometheus.Gauge
	circuitBreakerTrips        prometheus.Counter
	backendCircuitBreakerState *prometheus.GaugeVec
	backendCircuitBreakerTrips *prometheus.CounterVec
	backendHealthStatus        *prometheus.GaugeVec
	backendHealthFailures      *prometheus.CounterVec
	backendHealthSuccesses     *prometheus.CounterVec
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
		rateLimitErrors: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "gateway_rate_limit_errors_total",
				Help: "Total number of rate limiter backend errors (e.g. Redis timeouts or connectivity issues).",
			},
		),
		rateLimitDegraded: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "gateway_rate_limit_degraded_total",
				Help: "Total number of requests allowed through due to rate limit fail-open policy.",
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
		backendHealthStatus: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "gateway_backend_health_status",
				Help: "Current active health status of the backend (0=healthy, 1=unhealthy, 2=recovering).",
			},
			[]string{"backend"},
		),
		backendHealthFailures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_backend_health_check_failures_total",
				Help: "Total number of failed health check probes for a backend.",
			},
			[]string{"backend"},
		),
		backendHealthSuccesses: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_backend_health_check_successes_total",
				Help: "Total number of successful health check probes for a backend.",
			},
			[]string{"backend"},
		),
	}

	m.circuitBreakerState.Set(float64(StateClosed))
	reg.MustRegister(
		m.requestsTotal,
		m.requestDuration,
		m.rateLimitRejections,
		m.rateLimitErrors,
		m.rateLimitDegraded,
		m.circuitBreakerState,
		m.circuitBreakerTrips,
		m.backendCircuitBreakerState,
		m.backendCircuitBreakerTrips,
		m.backendHealthStatus,
		m.backendHealthFailures,
		m.backendHealthSuccesses,
	)
	return m
}

// InstrumentHandler wraps an http.Handler with metrics tracking for application requests.
// Scrapes to /metrics are excluded from request counting and latency tracking.
func (m *GatewayMetrics) InstrumentHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Do not count the /metrics scrape itself in application request metrics
		if r.URL.Path == "/metrics" || r.URL.Path == "/health" || reservedManagementPath(r.URL.Path) {
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

// RequestLogEntry holds structured metadata about a processed HTTP request.
// Sensitive headers (API keys, Authorization, passwords) and bodies are strictly omitted.
type RequestLogEntry struct {
	ID              string  `json:"id"`
	Method          string  `json:"method"`
	Path            string  `json:"path"`
	Status          int     `json:"status"`
	DurationMs      float64 `json:"durationMs"`
	Backend         string  `json:"backend"`
	Timestamp       string  `json:"timestamp"`
	FailureCategory string  `json:"failureCategory,omitempty"`
}

// RequestLogger stores recent request logs in a thread-safe, memory-bounded circular buffer.
type RequestLogger struct {
	mu       sync.RWMutex
	capacity int
	entries  []RequestLogEntry
	cursor   int
	count    int
}

// NewRequestLogger creates a new bounded RequestLogger with the given capacity.
func NewRequestLogger(capacity int) *RequestLogger {
	if capacity <= 0 {
		capacity = 500
	}
	return &RequestLogger{
		capacity: capacity,
		entries:  make([]RequestLogEntry, capacity),
	}
}

// Record atomically appends an entry to the circular buffer.
func (l *RequestLogger) Record(entry RequestLogEntry) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	l.entries[l.cursor] = entry
	l.cursor = (l.cursor + 1) % l.capacity
	if l.count < l.capacity {
		l.count++
	}
}

// GetEntries returns recent logs filtered by status, method, and substring search, ordered from newest to oldest.
func (l *RequestLogger) GetEntries(limit int, statusFilter int, methodFilter string, search string) []RequestLogEntry {
	if l == nil {
		return []RequestLogEntry{}
	}
	l.mu.RLock()
	defer l.mu.RUnlock()

	if limit <= 0 || limit > l.count {
		limit = l.count
	}

	result := make([]RequestLogEntry, 0, limit)
	methodFilter = strings.ToUpper(strings.TrimSpace(methodFilter))
	search = strings.ToLower(strings.TrimSpace(search))

	// Iterate backwards from newest to oldest
	for i := 0; i < l.count && len(result) < limit; i++ {
		idx := (l.cursor - 1 - i + l.capacity) % l.capacity
		entry := l.entries[idx]

		if statusFilter > 0 && entry.Status != statusFilter {
			continue
		}
		if methodFilter != "" && entry.Method != methodFilter {
			continue
		}
		if search != "" {
			if !strings.Contains(strings.ToLower(entry.ID), search) &&
				!strings.Contains(strings.ToLower(entry.Path), search) &&
				!strings.Contains(strings.ToLower(entry.Backend), search) {
				continue
			}
		}

		result = append(result, entry)
	}

	return result
}

// TotalCount returns the current count of stored entries in the circular buffer.
func (l *RequestLogger) TotalCount() int {
	if l == nil {
		return 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.count
}

// validateOrGenerateRequestID ensures the incoming Request ID meets safe character and length bounds.
// If valid, the incoming ID is accepted; otherwise, a fresh RFC4122 UUID v4 is generated.
func validateOrGenerateRequestID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw != "" && len(raw) <= 64 {
		valid := true
		for i := 0; i < len(raw); i++ {
			c := raw[i]
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
				valid = false
				break
			}
		}
		if valid {
			return raw
		}
	}
	return generateUUID()
}

// generateUUID generates a cryptographically secure random RFC4122 UUID v4.
func generateUUID() string {
	var b [16]byte
	_, err := rand.Read(b[:])
	if err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC4122 variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// determineFailureCategory classifies the response status into a high-level failure category.
func determineFailureCategory(statusCode int) string {
	switch statusCode {
	case http.StatusUnauthorized:
		return "auth_failed"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusMethodNotAllowed:
		return "method_not_allowed"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusBadGateway:
		return "bad_gateway"
	case http.StatusServiceUnavailable:
		return "service_unavailable"
	case http.StatusGatewayTimeout:
		return "gateway_timeout"
	default:
		if statusCode >= 500 {
			return "server_error"
		}
		if statusCode >= 400 {
			return "client_error"
		}
		return ""
	}
}

// requestInspectorMiddleware injects unique Request IDs and records structured logs into the bounded buffer.
func requestInspectorMiddleware(logger *RequestLogger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := validateOrGenerateRequestID(r.Header.Get("X-Request-ID"))
		w.Header().Set("X-Request-ID", reqID)
		r.Header.Set("X-Request-ID", reqID)

		start := time.Now()
		srw, ok := w.(*statusResponseWriter)
		if !ok {
			srw = newStatusResponseWriter(w)
		}

		next.ServeHTTP(srw, r)

		// Do not record internal telemetry polls to avoid flooding the bounded buffer
		if r.URL.Path == "/metrics" || r.URL.Path == "/health" || reservedManagementPath(r.URL.Path) {
			return
		}

		duration := float64(time.Since(start).Microseconds()) / 1000.0 // in ms

		backend := srw.Header().Get("X-Backend-Id")
		if backend == "" {
			if r.URL.Path == "/health" || srw.statusCode == http.StatusUnauthorized || srw.statusCode == http.StatusTooManyRequests {
				backend = "gateway"
			} else {
				backend = "none"
			}
		}

		failureCategory := determineFailureCategory(srw.statusCode)
		slog.Info("gateway_request", "request_id", reqID, "method", r.Method,
			"status", srw.statusCode, "duration_ms", duration, "backend", backend,
			"failure_category", failureCategory)

		if logger != nil {
			logger.Record(RequestLogEntry{
				ID:              reqID,
				Method:          r.Method,
				Path:            r.URL.Path, // Path only, query params stripped to avoid leaking credentials
				Status:          srw.statusCode,
				DurationMs:      duration,
				Backend:         backend,
				Timestamp:       start.UTC().Format(time.RFC3339),
				FailureCategory: failureCategory,
			})
		}
	})
}

// GatewayOption configures optional behavior of the gateway HTTP handler.
type GatewayOption func(*gatewayConfig)

type gatewayConfig struct {
	maxBodyBytes     int64
	redisFailureMode string // "fail-closed" (default) or "fail-open"
	metrics          *GatewayMetrics
	logger           *RequestLogger
	managementStore  *ManagementStore
	managementAuth   *ManagementAuth
	routeRegistry    *RouteRegistry
}

func WithMaxBodyBytes(bytes int64) GatewayOption {
	return func(c *gatewayConfig) {
		c.maxBodyBytes = bytes
	}
}

func WithRedisFailureMode(mode string) GatewayOption {
	return func(c *gatewayConfig) {
		if strings.EqualFold(mode, "fail-open") {
			c.redisFailureMode = "fail-open"
		} else {
			c.redisFailureMode = "fail-closed"
		}
	}
}

func WithMetrics(m *GatewayMetrics) GatewayOption {
	return func(c *gatewayConfig) {
		c.metrics = m
	}
}

func WithRequestLogger(logger *RequestLogger) GatewayOption {
	return func(c *gatewayConfig) {
		c.logger = logger
	}
}

func WithManagementStore(store *ManagementStore) GatewayOption {
	return func(c *gatewayConfig) {
		c.managementStore = store
	}
}

func WithManagementAuth(auth *ManagementAuth) GatewayOption {
	return func(c *gatewayConfig) { c.managementAuth = auth }
}

func WithRouteRegistry(registry *RouteRegistry) GatewayOption {
	return func(c *gatewayConfig) { c.routeRegistry = registry }
}

// newGatewayHandler constructs the HTTP routing and middleware for Gatekeeper with backward-compatible defaults.
func newGatewayHandler(apiKey string, proxy http.Handler, limiter RateLimiter, metrics ...*GatewayMetrics) http.Handler {
	var m *GatewayMetrics
	if len(metrics) > 0 && metrics[0] != nil {
		m = metrics[0]
	}
	return newGatewayHandlerWithOptions(apiKey, proxy, limiter, WithMetrics(m))
}

// newGatewayHandlerWithOptions constructs the HTTP routing and middleware for Gatekeeper with explicit options.
func newGatewayHandlerWithOptions(apiKey string, proxy http.Handler, limiter RateLimiter, opts ...GatewayOption) http.Handler {
	cfg := &gatewayConfig{
		maxBodyBytes:     10 * 1024 * 1024, // 10MB default
		redisFailureMode: "fail-closed",    // fail-closed by default
		logger:           NewRequestLogger(500),
	}
	for _, opt := range opts {
		opt(cfg)
	}

	m := cfg.metrics
	if m == nil {
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
	var managementPool *BackendPool
	if pool, ok := proxy.(*BackendPool); ok {
		managementPool = pool
	}
	routeRegistry := cfg.routeRegistry
	if routeRegistry == nil && managementPool != nil {
		routeRegistry = NewRouteRegistry(managementPool)
	}
	managementAuth := cfg.managementAuth
	if cfg.managementStore == nil {
		managementAuth = nil
	} else if managementAuth == nil {
		managementAuth = NewManagementAuth(cfg.managementStore, apiKey, true, limiter)
	}
	registerAuthenticationRoutes(mux, managementAuth)
	registerManagementRoutes(mux, cfg.managementStore, managementPool, routeRegistry, managementAuth, limiter)

	// Administrative Management Endpoint: Request Logs / Inspector
	handleRequests := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		var principal ManagementPrincipal
		var authErr error
		if managementAuth != nil {
			principal, authErr = managementAuth.Authenticate(r)
		} else if apiKey != "" && r.Header.Get("X-API-Key") != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-API-Key")), []byte(apiKey)) == 1 {
			// Keep the legacy inspector key behavior when persistent management auth is not configured.
			principal = ManagementPrincipal{ID: "legacy", Email: "legacy_admin", Role: "admin"}
		} else {
			authErr = errors.New("authentication required")
		}
		if authErr != nil || (managementAuth != nil && !managementAuth.CanRead(principal)) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		limit := 50
		if lStr := r.URL.Query().Get("limit"); lStr != "" {
			if l, err := strconv.Atoi(lStr); err == nil && l > 0 {
				if l > 200 {
					l = 200
				}
				limit = l
			}
		}

		statusFilter := 0
		if sStr := r.URL.Query().Get("status"); sStr != "" {
			if s, err := strconv.Atoi(sStr); err == nil {
				statusFilter = s
			}
		}

		methodFilter := r.URL.Query().Get("method")
		search := r.URL.Query().Get("search")

		entries := cfg.logger.GetEntries(limit, statusFilter, methodFilter, search)
		if entries == nil {
			entries = []RequestLogEntry{}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"requests": entries,
			"count":    len(entries),
		})
	}
	mux.HandleFunc("/api/requests", handleRequests)
	mux.HandleFunc("/api/requests/", handleRequests)

	// Administrative Management Endpoint: Upstream Backend Health Diagnostics
	handleBackends := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		providedKey := r.Header.Get("X-API-Key")
		if apiKey == "" || providedKey == "" || subtle.ConstantTimeCompare([]byte(providedKey), []byte(apiKey)) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		type backendResp struct {
			ID                   string `json:"id"`
			URL                  string `json:"url"`
			HealthStatus         string `json:"healthStatus"`
			CircuitBreakerState  string `json:"circuitBreakerState"`
			ConsecutiveFailures  int    `json:"consecutiveFailures"`
			ConsecutiveSuccesses int    `json:"consecutiveSuccesses"`
			Weight               int64  `json:"weight"`
			ActiveRequests       int64  `json:"activeRequests"`
			Strategy             string `json:"strategy"`
			LastCheckTime        string `json:"lastCheckTime,omitempty"`
			LastError            string `json:"lastError,omitempty"`
		}

		var backendsList []backendResp
		if pool, ok := proxy.(*BackendPool); ok {
			for _, n := range pool.Nodes() {
				st, fails, succs, lastCheck, lastErr := n.HealthInfo()
				cbState := "closed"
				if n.cb != nil {
					cbState = strings.ToLower(n.cb.State().String())
				}
				var lastCheckStr string
				if !lastCheck.IsZero() {
					lastCheckStr = lastCheck.Format(time.RFC3339)
				}
				backendsList = append(backendsList, backendResp{
					ID:                   n.Name(),
					URL:                  n.URL().String(),
					HealthStatus:         string(st),
					CircuitBreakerState:  cbState,
					ConsecutiveFailures:  fails,
					ConsecutiveSuccesses: succs,
					Weight:               pool.Weight(n),
					ActiveRequests:       n.ActiveRequests(),
					Strategy:             string(pool.Strategy()),
					LastCheckTime:        lastCheckStr,
					LastError:            lastErr,
				})
			}
		}

		if backendsList == nil {
			backendsList = []backendResp{}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"backends": backendsList,
		})
	}
	if cfg.managementStore == nil {
		mux.HandleFunc("/api/backends", handleBackends)
		mux.HandleFunc("/api/backends/", handleBackends)
	}

	// The raw scrape endpoint stays private to the Docker network. The dashboard
	// receives the same exposition only through this authenticated management path.
	mux.HandleFunc("/api/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		identity := "legacy"
		if managementAuth != nil {
			principal, err := managementAuth.Authenticate(r)
			if err != nil || !managementAuth.CanRead(principal) {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			identity = principal.ID
		} else if apiKey == "" || r.Header.Get("X-API-Key") == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-API-Key")), []byte(apiKey)) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if limiter != nil {
			result, err := allowManagementRequest(r.Context(), limiter, "management-metrics:"+hashAPIKey(identity))
			if err != nil {
				http.Error(w, "Metrics rate limiter unavailable", http.StatusServiceUnavailable)
				return
			}
			if !result.Allowed {
				w.Header().Set("Retry-After", strconv.FormatInt(result.RetryAfter, 10))
				http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
				return
			}
		}
		if managementPool != nil {
			managementPool.UpdateMetrics()
		}
		promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
	})

	// Protect API routes with API key authentication and rate limiting.
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		var route ManagedRoute
		routeMatched := false
		if routeRegistry != nil {
			var methodAllowed bool
			route, routeMatched, methodAllowed = routeRegistry.Match(r.URL.Path, r.Method)
			if routeMatched && !methodAllowed {
				w.Header().Set("Allow", strings.Join(route.Methods, ", "))
				http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
				return
			}
		}

		if cfg.maxBodyBytes > 0 && r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, cfg.maxBodyBytes)
		}

		providedKey := r.Header.Get("X-API-Key")

		// 1. Authenticate before rate limiting or forwarding. The bootstrap key is
		// retained for compatibility; managed keys are validated against PostgreSQL.
		var principal *APIKeyPrincipal
		staticKeyValid := apiKey != "" && providedKey != "" && subtle.ConstantTimeCompare([]byte(providedKey), []byte(apiKey)) == 1
		authRequired := !routeMatched || route.AuthRequired
		if authRequired && (providedKey == "" || (!staticKeyValid && cfg.managementStore == nil)) {
			log.Printf("auth=unauthorized path=%s", r.URL.Path)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if authRequired && !staticKeyValid && cfg.managementStore != nil {
			validated, err := cfg.managementStore.ValidateAPIKey(r.Context(), providedKey)
			if errors.Is(err, ErrAPIKeyNotFound) {
				log.Printf("auth=unauthorized path=%s", r.URL.Path)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			if err != nil {
				log.Printf("auth=validation_error path=%s error=%v", r.URL.Path, err)
				http.Error(w, "Service Unavailable: authentication store unavailable", http.StatusServiceUnavailable)
				return
			}
			principal = &validated
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				allowed := false
				for _, permission := range principal.Permissions {
					if permission == "write" {
						allowed = true
						break
					}
				}
				if !allowed {
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				}
			}
		}

		// 2. Derive rate-limit identity: SHA-256 hash (never store or log raw key).
		identity := providedKey
		if !authRequired {
			identity = "anonymous:" + remoteClientIdentity(r.RemoteAddr, r.Header.Get("X-Real-IP"))
		}
		keyHash := hashAPIKey(identity)

		// 3. Apply rate limiting: check Redis counter atomically.
		var res RateLimitResult
		var limitErr error
		if limiter != nil {
			if principal != nil {
				if policyLimiter, ok := limiter.(PolicyRateLimiter); ok {
					window := time.Duration(principal.WindowSeconds) * time.Second
					res, limitErr = policyLimiter.AllowWithPolicy(r.Context(), "managed:"+principal.ID, int64(principal.RequestsPerWindow), window)
				} else {
					res, limitErr = limiter.Allow(r.Context(), keyHash)
				}
			} else {
				res, limitErr = limiter.Allow(r.Context(), keyHash)
			}
		}
		if limiter != nil && limitErr == nil && routeMatched && route.RequestsPerWindow > 0 {
			policyLimiter, ok := limiter.(PolicyRateLimiter)
			if !ok {
				limitErr = errors.New("rate limiter does not support route policies")
			} else {
				routeIdentity := "route:" + route.ID + ":" + keyHash
				var routeResult RateLimitResult
				routeResult, limitErr = policyLimiter.AllowWithPolicy(r.Context(), routeIdentity,
					int64(route.RequestsPerWindow), time.Duration(route.WindowSeconds)*time.Second)
				if limitErr == nil {
					res = routeResult
				}
			}
		}

		if limitErr != nil {
			if m != nil && m.rateLimitErrors != nil {
				m.rateLimitErrors.Inc()
			}
			if cfg.redisFailureMode == "fail-open" {
				log.Printf("rate_limit=degraded_allowed path=%s error=%v", r.URL.Path, limitErr)
				if m != nil && m.rateLimitDegraded != nil {
					m.rateLimitDegraded.Inc()
				}
				w.Header().Set("X-RateLimit-Degraded", "true")
			} else {
				// Fail safely: do not silently bypass rate limiting when Redis is unavailable.
				log.Printf("rate_limit=error path=%s error=%v", r.URL.Path, limitErr)
				http.Error(w, "Service Unavailable: rate limiter unavailable", http.StatusServiceUnavailable)
				return
			}
		} else if limiter != nil {
			w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(res.Limit, 10))
			w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(res.Remaining, 10))
			if res.RetryAfter > 0 {
				resetTime := time.Now().Add(time.Duration(res.RetryAfter) * time.Second).Unix()
				w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetTime, 10))
			}

			if !res.Allowed {
				// Rate limit exceeded.
				log.Printf("rate_limit=exceeded path=%s retry_after=%d", r.URL.Path, res.RetryAfter)
				w.Header().Set("Retry-After", strconv.FormatInt(res.RetryAfter, 10))
				if res.RetryAfter > 0 {
					resetTime := time.Now().Add(time.Duration(res.RetryAfter) * time.Second).Unix()
					w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetTime, 10))
				}
				http.Error(w, fmt.Sprintf("Too Many Requests: rate limit exceeded. Retry after %d seconds", res.RetryAfter), http.StatusTooManyRequests)
				return
			}

			// Allowed.
			log.Printf("rate_limit=allowed path=%s remaining=%d", r.URL.Path, res.Remaining)
		}

		// 4. Forward authenticated and rate-permitted request to backend.
		w.Header().Set("X-Gateway", "Gatekeeper")
		if routeMatched {
			node := routeRegistry.Backend(route)
			if node == nil {
				http.Error(w, "Service Unavailable: route backend is not active", http.StatusServiceUnavailable)
			} else {
				managementPool.ServeTarget(w, r, node)
			}
		} else {
			proxy.ServeHTTP(w, r)
		}

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

	var handler http.Handler = mux
	handler = m.InstrumentHandler(handler)
	handler = requestInspectorMiddleware(cfg.logger, handler)
	handler = traceRequests(handler)
	return handler
}

// securityHeadersMiddleware injects standard defense-in-depth HTTP security headers.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware wraps an http.Handler with Cross-Origin Resource Sharing (CORS) headers.
func corsMiddleware(allowedOriginsStr string, next http.Handler) http.Handler {
	var allowedOrigins []string
	allowAll := false
	trimmed := strings.TrimSpace(allowedOriginsStr)
	if trimmed == "*" {
		allowAll = true
	} else {
		for _, o := range strings.Split(trimmed, ",") {
			if o = strings.TrimSpace(o); o != "" {
				if o == "*" {
					allowAll = true
					break
				}
				allowedOrigins = append(allowedOrigins, o)
			}
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		var matchedOrigin string

		if allowAll {
			matchedOrigin = "*"
		} else if origin != "" {
			for _, allowed := range allowedOrigins {
				if strings.EqualFold(origin, allowed) {
					matchedOrigin = origin
					break
				}
			}
		}

		if matchedOrigin != "" {
			w.Header().Set("Access-Control-Allow-Origin", matchedOrigin)
			if matchedOrigin != "*" {
				w.Header().Add("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS")
			if matchedOrigin != "*" {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			w.Header().Set("Access-Control-Allow-Headers", "X-API-Key, Content-Type, Authorization, Accept, Origin, X-Requested-With, X-Request-ID")
			w.Header().Set("Access-Control-Expose-Headers", "X-Backend-Id, X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset, X-RateLimit-Degraded, X-Request-ID, Retry-After, X-Gateway")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}

		// Handle preflight OPTIONS request before hitting auth/rate-limiting/proxy
		if r.Method == http.MethodOptions {
			if origin != "" && matchedOrigin == "" && !allowAll {
				http.Error(w, "Forbidden: CORS origin not allowed", http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
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

func getEnvBool(key string, defaultVal bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultVal
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return defaultVal
	}
	return parsed
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
		if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
			return nil, fmt.Errorf("invalid backend URL %q: HTTP(S) scheme and host required; credentials and fragments are not allowed", trimmed)
		}
		urls = append(urls, u)
	}

	if len(urls) == 0 {
		return nil, errors.New("no valid backend URLs provided")
	}
	return urls, nil
}

func applyBackendWeights(pool *BackendPool, raw string) error {
	if pool == nil || strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	nodes := pool.Nodes()
	if len(parts) != len(nodes) {
		return fmt.Errorf("BACKEND_WEIGHTS has %d values for %d configured backends", len(parts), len(nodes))
	}
	weights := make([]int64, len(parts))
	for i, part := range parts {
		weight, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil {
			return fmt.Errorf("invalid backend weight %q: %w", strings.TrimSpace(part), err)
		}
		if weight < 1 || weight > 1000 {
			return fmt.Errorf("invalid weight for backend %s: weight must be between 1 and 1000", nodes[i].Name())
		}
		weights[i] = weight
	}
	configured := make(map[string]int64, len(weights))
	for i, weight := range weights {
		configured[nodes[i].Name()] = weight
	}
	return pool.Configure(pool.Strategy(), configured)
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	traceShutdown, err := initializeTracing(context.Background())
	if err != nil {
		log.Fatalf("tracing initialization failed: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := traceShutdown(ctx); err != nil {
			log.Printf("tracing shutdown failed: %v", err)
		}
	}()
	// Read the API key from the environment.
	apiKey := os.Getenv("GATEKEEPER_API_KEY")
	if apiKey == "" {
		log.Fatal("GATEKEEPER_API_KEY must be set")
	}

	var managementDB *sql.DB
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		dbCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		db, err := OpenManagementDB(dbCtx, databaseURL)
		cancel()
		if err != nil {
			log.Fatalf("management database initialization failed: %v", err)
		}
		managementDB = db
		defer managementDB.Close()
		log.Printf("PostgreSQL management schema is ready")
		adminEmail, adminPassword := strings.TrimSpace(os.Getenv("DASHBOARD_ADMIN_EMAIL")), os.Getenv("DASHBOARD_ADMIN_PASSWORD")
		if (adminEmail == "") != (adminPassword == "") {
			log.Fatal("DASHBOARD_ADMIN_EMAIL and DASHBOARD_ADMIN_PASSWORD must both be configured")
		}
		if adminEmail != "" {
			normalizedEmail, err := normalizeDashboardCredentials(adminEmail, adminPassword)
			if err != nil {
				log.Fatalf("dashboard administrator configuration: %v", err)
			}
			passwordHash, err := hashDashboardPassword(adminPassword)
			if err != nil {
				log.Fatalf("dashboard administrator password setup failed: %v", err)
			}
			seedCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, err = NewManagementStore(managementDB).BootstrapDashboardAdmin(seedCtx, normalizedEmail, passwordHash)
			cancel()
			if err != nil && !errors.Is(err, ErrDashboardSetupComplete) {
				log.Fatalf("dashboard administrator setup failed: %v", err)
			}
		}
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

	backendTimeout, err := parseEnvDurationPositive("BACKEND_TIMEOUT", 10*time.Second)
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	backendTransport := http.DefaultTransport.(*http.Transport).Clone()
	backendTransport.ResponseHeaderTimeout = backendTimeout

	nodes := make([]*BackendNode, 0, len(backendURLs))
	for _, u := range backendURLs {
		cb := NewCircuitBreaker(cbThreshold, cbCooldown)
		node := NewBackendNode(u, cb, backendTransport)
		nodes = append(nodes, node)
	}

	pool, err := NewBackendPool(nodes)
	if err != nil {
		log.Fatalf("failed to initialize backend pool: %v", err)
	}
	if err := applyBackendWeights(pool, os.Getenv("BACKEND_WEIGHTS")); err != nil {
		log.Fatalf("backend weight configuration error: %v", err)
	}
	strategy := LoadBalancingStrategy(getEnv("LOAD_BALANCE_STRATEGY", string(StrategyRoundRobin)))
	if err := pool.SetStrategy(strategy); err != nil {
		log.Fatalf("load balancing configuration error: %v", err)
	}
	if managementDB != nil {
		managementStore := NewManagementStore(managementDB)
		backendsCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := bootstrapManagedBackends(backendsCtx, managementStore, pool); err != nil {
			cancel()
			log.Fatalf("load persistent backend configuration: %v", err)
		}
		cancel()
		settingsCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		settings, found, err := managementStore.LoadLoadBalancingSettings(settingsCtx)
		cancel()
		if err != nil {
			log.Fatalf("load persisted load balancing settings: %v", err)
		}
		if found {
			knownWeights := make(map[string]int64, len(settings.Weights))
			knownNames := make(map[string]struct{})
			for _, node := range pool.Nodes() {
				knownNames[node.Name()] = struct{}{}
			}
			for name, weight := range settings.Weights {
				if _, known := knownNames[name]; known {
					knownWeights[name] = weight
				} else {
					log.Printf("ignoring persisted weight for removed backend %q", name)
				}
			}
			settings.Weights = knownWeights
			if err := pool.Configure(settings.Strategy, settings.Weights); err != nil {
				log.Fatalf("invalid persisted load balancing settings: %v", err)
			}
			log.Printf("Loaded persisted load balancing strategy: %s", pool.Strategy())
		}
	}
	routeRegistry := NewRouteRegistry(pool)
	if managementDB != nil {
		routesCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		persistedRoutes, err := NewManagementStore(managementDB).ListRoutes(routesCtx)
		cancel()
		if err != nil {
			log.Fatalf("load persistent route configuration: %v", err)
		}
		if err := routeRegistry.Replace(persistedRoutes); err != nil {
			log.Fatalf("invalid persistent route configuration: %v", err)
		}
	}

	// Redis connection settings. Supports REDIS_URL (e.g. from Render Key Value or Upstash),
	// falling back to REDIS_ADDR, REDIS_PASSWORD, and REDIS_DB.
	var rdb *redis.Client
	redisSource := "REDIS_ADDR"
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		opt, err := redis.ParseURL(redisURL)
		if err != nil {
			log.Fatalf("invalid REDIS_URL: %v", err)
		}
		rdb = redis.NewClient(opt)
		redisSource = "REDIS_URL"
	} else {
		redisAddr := getEnv("REDIS_ADDR", "localhost:6379")
		redisPassword := os.Getenv("REDIS_PASSWORD")
		redisDB := getEnvInt("REDIS_DB", 0)

		rdb = redis.NewClient(&redis.Options{
			Addr:     redisAddr,
			Password: redisPassword,
			DB:       redisDB,
		})
		redisSource = redisAddr
	}
	defer rdb.Close()

	// Rate limiter configuration: 10 requests per 60 seconds by default.
	maxRequests, err := parseEnvIntPositive("RATE_LIMIT_MAX_REQUESTS", 10)
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}
	window, err := parseEnvDurationPositive("RATE_LIMIT_WINDOW", 60*time.Second)
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}
	keyPrefix := getEnv("REDIS_KEY_PREFIX", "gatekeeper:rate_limit:")

	limiter := NewRedisRateLimiter(rdb, keyPrefix, int64(maxRequests), window)
	metrics := NewGatewayMetrics(prometheus.NewRegistry())
	pool.SetMetrics(metrics)

	rateLimitFailureMode := getEnv("RATE_LIMIT_FAILURE_MODE", "fail-closed")
	maxBodyBytes := int64(getEnvInt("MAX_REQUEST_BODY_BYTES", 10*1024*1024))

	maxLogEntries := getEnvInt("MAX_LOG_ENTRIES", 500)
	logger := NewRequestLogger(maxLogEntries)
	stopHistory, err := startRequestHistory(NewManagementStore(managementDB), logger, getEnv("GATEWAY_INSTANCE_ID", "default"))
	if err != nil {
		log.Fatalf("request history restore failed: %v", err)
	}
	defer stopHistory()

	gwHandler := newGatewayHandlerWithOptions(
		apiKey,
		pool,
		limiter,
		WithMetrics(metrics),
		WithRedisFailureMode(rateLimitFailureMode),
		WithMaxBodyBytes(maxBodyBytes),
		WithRequestLogger(logger),
		WithManagementStore(NewManagementStore(managementDB)),
		WithManagementAuth(NewManagementAuth(NewManagementStore(managementDB), apiKey, getEnvBool("DASHBOARD_SESSION_COOKIE_SECURE", true), limiter)),
		WithRouteRegistry(routeRegistry),
	)
	corsOrigins := getEnv("CORS_ALLOWED_ORIGINS", "")
	corsHandler := corsMiddleware(corsOrigins, gwHandler)
	handler := securityHeadersMiddleware(corsHandler)

	healthCheckInterval := getEnvDuration("HEALTH_CHECK_INTERVAL", 5*time.Second)
	healthCheckPath := getEnv("HEALTH_CHECK_PATH", "/health")
	healthCheckTimeout := getEnvDuration("HEALTH_CHECK_TIMEOUT", 2*time.Second)
	unhealthyThreshold := getEnvInt("HEALTH_CHECK_UNHEALTHY_THRESHOLD", 3)
	recoveryThreshold := getEnvInt("HEALTH_CHECK_RECOVERY_THRESHOLD", 2)

	healthChecker := NewHealthChecker(pool, healthCheckInterval, healthCheckPath, healthCheckTimeout, unhealthyThreshold, recoveryThreshold)
	if healthChecker != nil {
		healthChecker.SetMetrics(metrics)
		healthChecker.Start()
		defer healthChecker.Stop()
		log.Printf("Active health checker started (interval: %s, path: %s, unhealthy: %d, recovery: %d)",
			healthCheckInterval, healthCheckPath, unhealthyThreshold, recoveryThreshold)
	}

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
	log.Printf("Gatekeeper running at http://localhost:%s (backends: %v, strategy: %s, redis: %s, cb_threshold: %d, cb_cooldown: %s, backend_timeout: %s)", port, backendStrs, pool.Strategy(), redisSource, cbThreshold, cbCooldown, backendTimeout)

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

func bootstrapManagedBackends(ctx context.Context, store *ManagementStore, pool *BackendPool) error {
	records, err := store.ListBackends(ctx)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		initial := make([]ManagedBackend, 0, len(pool.Nodes()))
		for index, node := range pool.Nodes() {
			initial = append(initial, ManagedBackend{
				Name: fmt.Sprintf("backend-%d", index+1), URL: node.URL().String(), Weight: pool.Weight(node),
				Enabled: true, HealthPath: "/health",
			})
		}
		if err := store.CreateBackends(ctx, initial); err != nil {
			return fmt.Errorf("persist environment backends: %w", err)
		}
		records, err = store.ListBackends(ctx)
		if err != nil {
			return err
		}
	}
	return applyManagedBackends(pool, records, store, ctx, false)
}
