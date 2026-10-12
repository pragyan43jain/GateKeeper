package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
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
	registry            *prometheus.Registry
	requestsTotal       *prometheus.CounterVec
	requestDuration     *prometheus.HistogramVec
	rateLimitRejections prometheus.Counter
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
	}

	reg.MustRegister(m.requestsTotal, m.requestDuration, m.rateLimitRejections)
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
	mux.Handle("/metrics", promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{}))

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

func main() {
	// Read the API key from the environment.
	apiKey := os.Getenv("GATEKEEPER_API_KEY")
	if apiKey == "" {
		log.Fatal("GATEKEEPER_API_KEY must be set")
	}

	// Configure the backend destination.
	backendURL := getEnv("BACKEND_URL", "http://localhost:8081")
	target, err := url.Parse(backendURL)
	if err != nil {
		log.Fatalf("invalid backend URL: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

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

	handler := newGatewayHandler(apiKey, proxy, limiter, metrics)

	port := getEnv("PORT", "8080")
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("Gatekeeper running at http://localhost:%s (backend: %s, redis: %s)", port, backendURL, redisAddr)
	log.Fatal(server.ListenAndServe())
}
