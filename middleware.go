// middleware.go
package r

import (
	"bytes"
	"context"
	"fmt"
	"github.com/google/uuid"
	"math"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Initialize the tags pool (add this global variable at the top of middleware.go)
var (
	tagsPool = sync.Pool{
		New: func() interface{} {
			return make(map[string]string, 2) // Preallocate with expected size
		},
	}
)

// MiddlewareProvider defines the interface for middleware functionality
type MiddlewareProvider interface {
	Logger(format string) MiddlewareFunc
	RateLimit(reqs int, per time.Duration) MiddlewareFunc
	Timeout(duration time.Duration) MiddlewareFunc
	Security() MiddlewareFunc
	Recovery(handler func(Context, interface{})) MiddlewareFunc
	Compression() MiddlewareFunc
	RequestID() MiddlewareFunc
	CORS(origins []string) MiddlewareFunc
}

// RateLimiter defines the interface for rate limiting strategies
type RateLimiter interface {
	Allow(key string) bool
	Reset(key string)
	SetDistributedClient(client RedisClient)
	GetQuota(key string) (remaining int, reset time.Time)
	GetDebugStats(key string) map[string]interface{} // Add this method

}

// SecurityProvider defines the interface for security header management
type SecurityProvider interface {
	SetHeaders(c Context)
}

type CORSConfig struct {
	Origins          []string
	AllowMethods     []string
	AllowHeaders     []string
	AllowCredentials bool
	MaxAge           int
}

// Add default CORS config function
func defaultCORSConfig() *CORSConfig {
	return &CORSConfig{
		Origins: []string{"*"},
		AllowMethods: []string{
			"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS",
		},
		AllowHeaders: []string{
			"Authorization", "Content-Type", "Accept", "Origin",
			"User-Agent", "DNT", "Cache-Control", "X-Mx-ReqToken",
			"Keep-Alive", "X-Requested-With", "If-Modified-Since",
		},
		AllowCredentials: true,
		MaxAge:           86400,
	}
}

// standardMiddleware implements MiddlewareProvider
type standardMiddleware struct {
	rateLimiter RateLimiter
	logger      Logger
	security    SecurityProvider
	corsConfig  *CORSConfig
	router      *RouterImpl
	metrics     MetricsCollector // Add this field
}

// NewMiddlewareProvider creates a new middleware provider with optional dependencies
func NewMiddlewareProvider(router *RouterImpl, opts ...MiddlewareOption) MiddlewareProvider {
	m := &standardMiddleware{
		router:     router,
		corsConfig: defaultCORSConfig(), // Use default config
	}

	// Apply any custom options
	for _, opt := range opts {
		opt(m)
	}

	return m
}

// MiddlewareOption defines functional options for middleware configuration
type MiddlewareOption func(*standardMiddleware)

// WithRateLimiter sets a custom rate limiter implementation
func WithRateLimiter(rl RateLimiter) MiddlewareOption {
	return func(m *standardMiddleware) {
		m.rateLimiter = rl
	}
}

// Default implementations

// Update constructor to include new fields
func NewDefaultRateLimiter(reqs int, per time.Duration, opts ...RateLimiterOption) RateLimiter {
	rl := &defaultRateLimiter{
		tokens:      make(map[string]float64),
		lastReq:     make(map[string]time.Time),
		maxRate:     float64(reqs),
		per:         per,
		cleanupTick: time.NewTicker(time.Minute * 5),
		keyPrefix:   "ratelimit",
		logger:      NewDefaultLogger(),
		buckets:     make(map[string]*tokenBucket),
		debugMode:   true, // Enable debug by default for investigation
	}

	// Apply options
	for _, opt := range opts {
		opt(rl)
	}

	go rl.cleanup()
	return rl
}

type RateLimiterOption func(*defaultRateLimiter)

func WithMetrics(metrics MetricsCollector) RateLimiterOption {
	return func(rl *defaultRateLimiter) {
		rl.metrics = metrics
	}
}

func (rl *defaultRateLimiter) cleanup() {
	for range rl.cleanupTick.C {
		rl.mu.Lock()
		now := time.Now()
		for key, bucket := range rl.buckets {
			// Convert atomic timestamp to time.Time
			lastRefillTime := time.Unix(0, bucket.lastRefill.Load())
			if now.Sub(lastRefillTime) > rl.per*2 {
				delete(rl.buckets, key)
				if rl.metrics != nil {
					rl.metrics.IncrementCounter("rate_limiter.bucket_cleaned",
						map[string]string{"key": key})
				}
			}
		}
		rl.mu.Unlock()
	}
}

// tokenBucket stores the rate limiting state
type tokenBucket struct {
	tokens     atomic.Int64 // Store tokens * 1_000_000
	lastRefill atomic.Int64 // Store UnixNano timestamp
	stats      struct {
		allowCount  atomic.Int64
		rejectCount atomic.Int64
		refillCount atomic.Int64
	}
}

const tokenScale = 1_000_000 // Scale factor for fixed-point arithmetic

type defaultRateLimiter struct {
	tokens      map[string]float64
	lastReq     map[string]time.Time
	maxRate     float64
	per         time.Duration
	mu          sync.RWMutex
	cleanupTick *time.Ticker
	redisClient RedisClient
	keyPrefix   string
	logger      Logger
	metrics     MetricsCollector
	buckets     map[string]*tokenBucket
	debugMode   bool
}

func (rl *defaultRateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()

	bucket, exists := rl.buckets[key]
	if !exists {
		// First request, create new bucket with full tokens
		bucket = &tokenBucket{}
		bucket.tokens.Store(int64(rl.maxRate * tokenScale))
		bucket.lastRefill.Store(now.UnixNano())
		rl.buckets[key] = bucket
		bucket.stats.allowCount.Add(1)

		if rl.metrics != nil {
			rl.metrics.IncrementCounter("rate_limiter.bucket_created",
				map[string]string{"key": key})
		}
		return true
	}

	// Calculate elapsed time since last refill with nanosecond precision
	lastRefillTime := time.Unix(0, bucket.lastRefill.Load())
	elapsed := now.Sub(lastRefillTime).Seconds()

	// Calculate tokens to add with high precision
	currentTokens := float64(bucket.tokens.Load()) / tokenScale
	tokensToAdd := elapsed * (rl.maxRate / rl.per.Seconds())

	// Add tokens and round up to prevent token loss from floating point rounding
	newTokens := math.Min(rl.maxRate, currentTokens+tokensToAdd)

	// Store new token count with scaling
	bucket.tokens.Store(int64(newTokens * tokenScale))
	bucket.lastRefill.Store(now.UnixNano())
	bucket.stats.refillCount.Add(1)

	// We need at least 1.0 tokens to allow the request
	if newTokens >= 1.0 {
		// Consume exactly one token
		bucket.tokens.Add(-tokenScale)
		bucket.stats.allowCount.Add(1)

		if rl.metrics != nil {
			rl.metrics.IncrementCounter("rate_limiter.request_allowed",
				map[string]string{
					"key":              key,
					"tokens_remaining": fmt.Sprintf("%.2f", newTokens-1),
				})
		}
		return true
	}

	bucket.stats.rejectCount.Add(1)
	if rl.metrics != nil {
		rl.metrics.IncrementCounter("rate_limiter.request_rejected",
			map[string]string{
				"key":              key,
				"tokens_remaining": fmt.Sprintf("%.2f", newTokens),
			})
	}
	return false
}

func (rl *defaultRateLimiter) localAllow(key string) bool {
	now := time.Now()
	if _, exists := rl.tokens[key]; !exists {
		rl.tokens[key] = rl.maxRate
		rl.lastReq[key] = now
		return true
	}

	elapsed := now.Sub(rl.lastReq[key]).Seconds()
	rl.tokens[key] += elapsed * (rl.maxRate / rl.per.Seconds())
	if rl.tokens[key] > rl.maxRate {
		rl.tokens[key] = rl.maxRate
	}

	if rl.tokens[key] < 1 {
		return false
	}

	rl.tokens[key]--
	rl.lastReq[key] = now
	return true
}

// Add distributed implementation
func (rl *defaultRateLimiter) distributedAllow(key string) bool {
	redisKey := fmt.Sprintf("%s:%s", rl.keyPrefix, key)

	// Use simpler implementation with existing RedisClient interface
	now := time.Now().UnixNano()
	windowStart := now - rl.per.Nanoseconds()

	// Remove old entries
	if err := rl.redisClient.ZRemRangeByScore(redisKey,
		"0",
		strconv.FormatInt(windowStart, 10)); err != nil {
		rl.logger.Error("Failed to remove old entries",
			"error", err,
			"key", key)
		return rl.localAllow(key)
	}

	// Add new request
	if err := rl.redisClient.ZAdd(redisKey,
		float64(now),
		strconv.FormatInt(now, 10)); err != nil {
		rl.logger.Error("Failed to add new request",
			"error", err,
			"key", key)
		return rl.localAllow(key)
	}

	// Get current count
	count, err := rl.redisClient.ZCount(redisKey,
		strconv.FormatInt(windowStart, 10),
		"+inf")
	if err != nil {
		rl.logger.Error("Failed to get request count",
			"error", err,
			"key", key)
		return rl.localAllow(key)
	}

	// Set expiration using existing Set method
	rl.redisClient.Set(redisKey+":exp", "", rl.per*2)

	// Update metrics if available
	if rl.metrics != nil {
		rl.metrics.RecordValue("rate_limiter.requests", float64(count),
			map[string]string{"key": key})
	}

	allowed := count <= int64(rl.maxRate)
	if !allowed && rl.metrics != nil {
		rl.metrics.IncrementCounter("rate_limiter.exceeded",
			map[string]string{"key": key})
	}

	return allowed
}

func (rl *defaultRateLimiter) GetQuota(key string) (remaining int, reset time.Time) {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	bucket, exists := rl.buckets[key]
	if !exists {
		return int(rl.maxRate), time.Now()
	}

	currentTokens := bucket.tokens.Load()
	lastRefillTime := time.Unix(0, bucket.lastRefill.Load())

	// Calculate tokens available
	remainingTokens := float64(currentTokens) / tokenScale

	// Calculate time until next token
	tokensPerNano := (rl.maxRate / rl.per.Seconds()) / float64(time.Second.Nanoseconds())
	timeToNextToken := time.Duration(float64(tokenScale) / (tokensPerNano * float64(time.Second.Nanoseconds())))

	return int(remainingTokens), lastRefillTime.Add(timeToNextToken)
}

func (rl *defaultRateLimiter) Reset(key string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Clean up the bucket
	delete(rl.buckets, key)

	// Clean up legacy maps for backward compatibility
	delete(rl.tokens, key)
	delete(rl.lastReq, key)

	// If using Redis, clean up distributed state
	if rl.redisClient != nil {
		redisKey := fmt.Sprintf("%s:%s", rl.keyPrefix, key)
		if err := rl.redisClient.Set(redisKey, "", 0); err != nil {
			rl.logger.Error("Failed to reset Redis key",
				"error", err,
				"key", key)
		}
	}

	if rl.metrics != nil {
		rl.metrics.IncrementCounter("rate_limiter.reset",
			map[string]string{
				"key":    key,
				"source": "api_call",
			})
	}
}

// Middleware implementations
func (m *standardMiddleware) Logger(format string) MiddlewareFunc {
	return func(c Context) {
		start := time.Now()
		path := c.Path()
		method := c.Method()

		c.Next()

		latency := time.Since(start)
		status := c.RequestCtx().Response.StatusCode()

		if m.logger != nil {
			m.logger.Log(method, status, latency, c.RealIP(), path)
		} else {
			if format == "" {
				format = "%s | %3d | %13v | %15s | %s"
			}
			fmt.Printf(format+"\n", method, status, latency, c.RealIP(), path)
		}
	}
}

func (m *standardMiddleware) RateLimit(reqs int, per time.Duration) MiddlewareFunc {
	if m.rateLimiter == nil {
		m.rateLimiter = NewDefaultRateLimiter(reqs, per)
	}

	return func(c Context) {
		ip := c.RealIP()
		if !m.rateLimiter.Allow(ip) {
			remaining, reset := m.rateLimiter.GetQuota(ip)
			c.SetHeader("X-RateLimit-Limit", fmt.Sprintf("%d", reqs))
			c.SetHeader("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
			c.SetHeader("X-RateLimit-Reset", fmt.Sprintf("%d", reset.Unix()))
			c.SetHeader("Retry-After", fmt.Sprintf("%d", int(time.Until(reset).Seconds())))
			c.ErrorResponse(429, "rate limit exceeded")
			return
		}
		c.Next()
	}
}

func (m *standardMiddleware) CORS(origins []string) MiddlewareFunc {
	return func(c Context) {
		ctx := c.RequestCtx()
		origin := string(ctx.Request.Header.Peek("Origin"))

		// Check if origin is allowed
		allowed := false
		for _, o := range origins {
			if o == "*" || o == origin {
				allowed = true
				break
			}
		}

		if !allowed {
			c.Next()
			return
		}

		// Check if this is a preflight request
		if string(ctx.Method()) == "OPTIONS" {
			// Set preflight headers
			ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
			ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, HEAD, OPTIONS")
			ctx.Response.Header.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, Origin, X-Requested-With")
			ctx.Response.Header.Set("Access-Control-Allow-Credentials", "true")
			ctx.Response.Header.Set("Access-Control-Max-Age", "86400")

			// Set status and return - do not call Next()
			ctx.Response.SetStatusCode(204)
			return
		}

		// For regular requests, set basic CORS headers
		ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
		ctx.Response.Header.Set("Access-Control-Allow-Credentials", "true")

		c.Next()
	}
}

func handlePreflightRequest(c Context, origins []string) {
	ctx := c.RequestCtx()
	origin := string(ctx.Request.Header.Peek("Origin"))

	// Check if origin is allowed
	if !isOriginAllowed(origin, origins) {
		return
	}

	// Set preflight response headers
	headers := ctx.Response.Header
	headers.Set("Access-Control-Allow-Origin", origin)
	headers.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, HEAD, OPTIONS")
	headers.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, Origin, X-Requested-With")
	headers.Set("Access-Control-Allow-Credentials", "true")
	headers.Set("Access-Control-Max-Age", "86400")

	// Set status code and stop processing
	ctx.SetStatusCode(204)
}

func handleSimpleRequest(c Context, origins []string) {
	ctx := c.RequestCtx()
	origin := string(ctx.Request.Header.Peek("Origin"))

	if !isOriginAllowed(origin, origins) {
		return
	}

	// Set simple response headers
	headers := ctx.Response.Header
	headers.Set("Access-Control-Allow-Origin", origin)
	headers.Set("Access-Control-Allow-Credentials", "true")
}

func isOriginAllowed(origin string, allowedOrigins []string) bool {
	for _, allowed := range allowedOrigins {
		if allowed == "*" || allowed == origin {
			return true
		}
	}
	return false
}

func (m *standardMiddleware) RequestID() MiddlewareFunc {
	return func(c Context) {
		requestID := c.RequestID()
		if requestID == "" {
			requestID = uuid.New().String()
			ctx := c.(*ContextImpl)
			ctx.requestID = requestID
		}
		c.SetHeader("X-Request-ID", requestID)
		c.Next()
	}
}

// WithLogger sets a custom logger implementation
func WithLogger(l Logger) MiddlewareOption {
	return func(m *standardMiddleware) {
		m.logger = l
	}
}

// WithSecurity sets a custom security provider implementation
func WithSecurity(s SecurityProvider) MiddlewareOption {
	return func(m *standardMiddleware) {
		m.security = s
	}
}

// WithCORSConfig sets custom CORS configuration
func WithCORSConfig(config *CORSConfig) MiddlewareOption {
	return func(m *standardMiddleware) {
		m.corsConfig = config
	}
}

// Add this method to the standardMiddleware struct implementation
func (m *standardMiddleware) Compression() MiddlewareFunc {
	return func(c Context) {
		// Get the underlying fasthttp context
		ctx := c.RequestCtx()

		// Enable automatic compression
		ctx.Response.Header.Set("Content-Encoding", "gzip")

		// Check if client accepts gzip
		if bytes.Contains(ctx.Request.Header.Peek("Accept-Encoding"), []byte("gzip")) {
			ctx.Response.Header.Set("Vary", "Accept-Encoding")
			// Enable gzip compression for this response
			ctx.Response.Header.Set("Content-Encoding", "gzip")
		}

		c.Next()
	}
}

func (m *standardMiddleware) Security() MiddlewareFunc {
	return func(c Context) {
		// Set security headers
		c.SetHeader("X-Content-Type-Options", "nosniff")
		c.SetHeader("X-Frame-Options", "DENY")
		c.SetHeader("X-XSS-Protection", "1; mode=block")
		c.SetHeader("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		c.SetHeader("Content-Security-Policy", "default-src 'self'")
		c.SetHeader("Referrer-Policy", "strict-origin-when-cross-origin")

		// If custom security provider exists, use it
		if m.security != nil {
			m.security.SetHeaders(c)
		}

		c.Next()
	}
}

func (m *standardMiddleware) Timeout(duration time.Duration) MiddlewareFunc {
	return func(c Context) {
		ctx, cancel := context.WithTimeout(c, duration)
		defer cancel()

		done := make(chan struct{})
		errCh := make(chan error, 1)

		go func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					errCh <- fmt.Errorf("panic in handler: %v", r)
				}
			}()
			c.Next()
			errCh <- c.Error()
		}()

		select {
		case <-ctx.Done():
			c.AbortWithError(http.StatusGatewayTimeout, fmt.Errorf("request timeout after %v: %w", duration, ctx.Err()))
			// Cleanup goroutine
			go func() {
				<-done
			}()
			return
		case err := <-errCh:
			if err != nil {
				c.AbortWithError(http.StatusInternalServerError, err)
			}
			return
		case <-done:
			return
		}
	}
}

type RedisClient interface {
	Get(key string) (string, error)
	Set(key string, value string, expiration time.Duration) error
	IncrBy(key string, value int64) (int64, error)
	// Add new methods for rate limiting
	ZRemRangeByScore(key string, min, max string) error
	ZAdd(key string, score float64, member string) error
	ZCount(key string, min, max string) (int64, error)
}

func (rl *defaultRateLimiter) SetDistributedClient(client RedisClient) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.redisClient = client
	rl.keyPrefix = "ratelimit" // Default prefix, could be made configurable
}

func (m *standardMiddleware) CircuitBreaker(opts ...CircuitBreakerOption) MiddlewareFunc {
	config := defaultCircuitBreakerConfig()
	for _, opt := range opts {
		opt(&config)
	}

	breakers := &sync.Map{}

	return func(c Context) {
		path := c.Path()
		method := c.Method()
		key := method + ":" + path

		// Get or create circuit breaker for this endpoint
		cbValue, _ := breakers.LoadOrStore(key, NewCircuitBreaker(
			config.Threshold,
			config.ResetTimeout,
			m.metrics,
		))
		circuitBreaker := cbValue.(*CircuitBreaker)

		// Check if circuit is open
		if circuitBreaker.IsOpen() {
			if m.metrics != nil {
				m.metrics.IncrementCounter("circuit_breaker.rejected",
					map[string]string{
						"path":   path,
						"method": method,
					})
			}

			c.AbortWithError(http.StatusServiceUnavailable,
				fmt.Errorf("circuit breaker open for %s %s", method, path))
			return
		}

		// Start timing the request
		start := time.Now()

		// Create context with timeout
		ctx, cancel := context.WithTimeout(c, config.Timeout)
		defer cancel()

		// Replace original context
		c.(*ContextImpl).ctx = ctx

		// Execute handler with panic recovery
		var handlerError error
		func() {
			defer func() {
				if r := recover(); r != nil {
					handlerError = fmt.Errorf("panic: %v", r)
					if m.metrics != nil {
						m.metrics.IncrementCounter("circuit_breaker.panics",
							map[string]string{
								"path":   path,
								"method": method,
							})
					}
					// Log stack trace for panic
					if m.logger != nil {
						m.logger.Error("Panic in circuit breaker handler",
							"error", handlerError,
							"stack", string(debug.Stack()),
							"path", path,
							"method", method)
					}
				}
			}()
			c.Next()
			if err := c.Error(); err != nil {
				handlerError = err
			}
		}()

		// Record metrics and update circuit breaker state
		duration := time.Since(start)

		if handlerError != nil || duration > config.Timeout {
			circuitBreaker.RecordFailure()

			if m.metrics != nil {
				tags := map[string]string{
					"path":   path,
					"method": method,
				}

				if handlerError != nil {
					tags["reason"] = handlerError.Error()
					tags["type"] = "error"
				} else {
					tags["reason"] = "timeout"
					tags["type"] = "timeout"
				}

				m.metrics.IncrementCounter("circuit_breaker.failures", tags)
				m.metrics.RecordTiming("circuit_breaker.error_latency",
					duration, tags)
			}

			// Log detailed error information
			if m.logger != nil {
				m.logger.Error("Circuit breaker failure",
					"error", handlerError,
					"duration", duration,
					"timeout", config.Timeout,
					"path", path,
					"method", method)
			}

			// Trigger immediate state monitoring on failure
			circuitBreaker.MonitorState()
		} else {
			circuitBreaker.RecordSuccess()

			if m.metrics != nil {
				tags := map[string]string{
					"path":   path,
					"method": method,
				}

				m.metrics.RecordTiming("circuit_breaker.success_latency",
					duration, tags)
				m.metrics.IncrementCounter("circuit_breaker.successes", tags)

				// Record response time histogram
				buckets := []float64{10, 50, 100, 200, 500, 1000} // ms
				for _, bucket := range buckets {
					if duration.Milliseconds() <= int64(bucket) {
						m.metrics.IncrementCounter(
							fmt.Sprintf("circuit_breaker.latency_le_%v", bucket),
							tags)
					}
				}
			}
		}

		// Add request completion metrics
		if m.metrics != nil {
			m.metrics.RecordValue("circuit_breaker.request_duration_seconds",
				duration.Seconds(),
				map[string]string{
					"path":   path,
					"method": method,
					"status": fmt.Sprintf("%d", c.RequestCtx().Response.StatusCode()),
				})
		}

		// Check for context cancellation
		if ctx.Err() != nil {
			if m.metrics != nil {
				m.metrics.IncrementCounter("circuit_breaker.context_cancelled",
					map[string]string{
						"path":   path,
						"method": method,
					})
			}
		}
	}
}

type CircuitBreakerConfig struct {
	Threshold        int64
	ResetTimeout     time.Duration
	Timeout          time.Duration
	HalfOpenRequests int64
}

type CircuitBreakerOption func(*CircuitBreakerConfig)

func defaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		Threshold:        5,
		ResetTimeout:     10 * time.Second,
		Timeout:          5 * time.Second,
		HalfOpenRequests: 2,
	}
}

func WithFailureThreshold(threshold int64) CircuitBreakerOption {
	return func(c *CircuitBreakerConfig) {
		c.Threshold = threshold
	}
}

func WithResetTimeout(timeout time.Duration) CircuitBreakerOption {
	return func(c *CircuitBreakerConfig) {
		c.ResetTimeout = timeout
	}
}

func (m *standardMiddleware) Recovery(handler func(Context, interface{})) MiddlewareFunc {
	return func(c Context) {
		defer func() {
			if rcv := recover(); rcv != nil {
				// Capture stack trace
				stack := debug.Stack()

				// Create error context
				errCtx := &ErrorContext{
					Error:     fmt.Errorf("%v", rcv),
					Stack:     string(stack),
					RequestID: c.RequestID(),
					Timestamp: time.Now(),
					Path:      c.Path(),
					Method:    c.Method(),
					ClientIP:  c.RealIP(),
					UserAgent: c.GetHeader("User-Agent"),
				}

				// Record error metrics
				if m.metrics != nil {
					m.metrics.IncrementCounter("panic_recovery",
						map[string]string{
							"path":   c.Path(),
							"method": c.Method(),
						})
				}

				// Log the error with full context
				if m.logger != nil {
					m.logger.Error("Panic recovered in request handler",
						"error", errCtx.Error,
						"stack", errCtx.Stack,
						"request_id", errCtx.RequestID,
						"path", errCtx.Path,
						"method", errCtx.Method,
						"client_ip", errCtx.ClientIP,
						"user_agent", errCtx.UserAgent)
				}

				// If custom handler provided, use it
				if handler != nil {
					handler(c, rcv)
					return
				}

				// Default error response
				status := http.StatusInternalServerError
				response := map[string]interface{}{
					"error":      "Internal Server Error",
					"request_id": errCtx.RequestID,
					"timestamp":  errCtx.Timestamp.Format(time.RFC3339),
				}

				// In development, include more details
				if m.isDevelopment() {
					response["debug"] = map[string]interface{}{
						"error": errCtx.Error.Error(),
						"stack": errCtx.Stack,
					}
				}

				c.JSON(status, response)
			}
		}()

		c.Next()
	}
}

type ErrorContext struct {
	Error     error
	Stack     string
	RequestID string
	Timestamp time.Time
	Path      string
	Method    string
	ClientIP  string
	UserAgent string
}

func (m *standardMiddleware) isDevelopment() bool {
	return os.Getenv("APP_ENV") == "development"
}

func (rl *defaultRateLimiter) GetDebugStats(key string) map[string]interface{} {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	bucket, exists := rl.buckets[key]
	if !exists {
		return map[string]interface{}{
			"exists": false,
		}
	}

	return map[string]interface{}{
		"exists":         true,
		"current_tokens": float64(bucket.tokens.Load()) / tokenScale,
		"allow_count":    bucket.stats.allowCount.Load(),
		"reject_count":   bucket.stats.rejectCount.Load(),
		"refill_count":   bucket.stats.refillCount.Load(),
		"last_refill":    time.Unix(0, bucket.lastRefill.Load()),
		"max_rate":       rl.maxRate,
		"period_seconds": rl.per.Seconds(),
	}
}
