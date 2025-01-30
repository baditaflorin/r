// middleware.go
package r

import (
	"bytes"
	"context"
	"fmt"
	"github.com/google/uuid"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
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

// standardMiddleware implements MiddlewareProvider
type standardMiddleware struct {
	rateLimiter RateLimiter
	logger      Logger
	security    SecurityProvider
	corsConfig  *CORSConfig
	router      *RouterImpl // Added for panic handler access
}

// NewMiddlewareProvider creates a new middleware provider with optional dependencies
func NewMiddlewareProvider(router *RouterImpl, opts ...MiddlewareOption) MiddlewareProvider {
	m := &standardMiddleware{
		router: router,
		corsConfig: &CORSConfig{
			Origins:          []string{"*"},
			AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
			AllowHeaders:     []string{"Authorization", "Content-Type"},
			AllowCredentials: true,
			MaxAge:           86400,
		},
	}
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

type defaultRateLimiter struct {
	tokens      map[string]float64
	lastReq     map[string]time.Time
	maxRate     float64
	per         time.Duration
	mu          sync.RWMutex
	cleanupTick *time.Ticker
	redisClient RedisClient
	keyPrefix   string
}

func NewDefaultRateLimiter(reqs int, per time.Duration) RateLimiter {
	rl := &defaultRateLimiter{
		tokens:      make(map[string]float64),
		lastReq:     make(map[string]time.Time),
		maxRate:     float64(reqs),
		per:         per,
		cleanupTick: time.NewTicker(time.Minute * 5),
	}

	// Start cleanup goroutine
	go rl.cleanup()
	return rl
}

func (rl *defaultRateLimiter) cleanup() {
	for range rl.cleanupTick.C {
		rl.mu.Lock()
		now := time.Now()
		for ip, lastSeen := range rl.lastReq {
			if now.Sub(lastSeen) > rl.per*2 {
				delete(rl.tokens, ip)
				delete(rl.lastReq, ip)
			}
		}
		rl.mu.Unlock()
	}
}

func (rl *defaultRateLimiter) Allow(key string) bool {
	if rl.redisClient != nil {
		return rl.distributedAllow(key)
	}
	return rl.localAllow(key)
}

// Original implementation becomes localAllow
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

	// Try to get current token count
	count, err := rl.redisClient.IncrBy(redisKey, -1)
	if err != nil {
		// Fallback to local rate limiting on Redis errors
		return rl.localAllow(key)
	}

	// Initialize if not exists
	if count < 0 {
		rl.redisClient.Set(redisKey,
			fmt.Sprintf("%d", int(rl.maxRate)),
			rl.per)
		return true
	}

	return count >= 0
}

// Implement GetQuota method
func (rl *defaultRateLimiter) GetQuota(key string) (remaining int, reset time.Time) {
	if rl.redisClient != nil {
		// Get distributed quota
		redisKey := fmt.Sprintf("%s:%s", rl.keyPrefix, key)
		countStr, err := rl.redisClient.Get(redisKey)
		if err == nil {
			count, _ := strconv.Atoi(countStr)
			return count, time.Now().Add(rl.per)
		}
	}

	// Get local quota
	rl.mu.RLock()
	tokens := rl.tokens[key]
	lastReq := rl.lastReq[key]
	rl.mu.RUnlock()

	return int(tokens), lastReq.Add(rl.per)
}

func (rl *defaultRateLimiter) Reset(key string) {
	delete(rl.tokens, key)
	delete(rl.lastReq, key)
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
		if !m.rateLimiter.Allow(c.RealIP()) {
			c.AbortWithError(http.StatusTooManyRequests, fmt.Errorf("rate limit exceeded"))
			return
		}
		c.Next()
	}
}

func (m *standardMiddleware) CORS(origins []string) MiddlewareFunc {
	// If custom origins provided, override default config
	if len(origins) > 0 {
		m.corsConfig.Origins = origins
	}

	return func(c Context) {
		origin := c.GetHeader("Origin")

		// Check if origin is allowed
		allowed := false
		for _, o := range m.corsConfig.Origins {
			if o == "*" || o == origin {
				allowed = true
				break
			}
		}

		if allowed {
			c.SetHeader("Access-Control-Allow-Origin", origin)
			c.SetHeader("Access-Control-Allow-Methods",
				strings.Join(m.corsConfig.AllowMethods, ","))
			c.SetHeader("Access-Control-Allow-Headers",
				strings.Join(m.corsConfig.AllowHeaders, ","))

			if m.corsConfig.AllowCredentials {
				c.SetHeader("Access-Control-Allow-Credentials", "true")
			}

			if m.corsConfig.MaxAge > 0 {
				c.SetHeader("Access-Control-Max-Age",
					strconv.Itoa(m.corsConfig.MaxAge))
			}

			// Handle preflight requests
			if c.Method() == "OPTIONS" {
				c.AbortWithError(http.StatusNoContent, nil)
				return
			}
		}

		c.Next()
	}
}

func (m *standardMiddleware) RequestID() MiddlewareFunc {
	return func(c Context) {
		requestID := c.RequestID()
		if requestID == "" {
			requestID = uuid.New().String()
			ctx := c.(*contextImpl)
			ctx.requestID = requestID
		}
		c.SetHeader("X-Request-ID", requestID)
		c.Next()
	}
}

func (m *standardMiddleware) Recovery(handler func(Context, interface{})) MiddlewareFunc {
	return func(c Context) {
		defer func() {
			if rcv := recover(); rcv != nil {
				if h := m.router.panicHandler; h != nil {
					h(c, rcv)
				} else if handler != nil {
					handler(c, rcv)
				} else {
					c.AbortWithError(http.StatusInternalServerError,
						fmt.Errorf("panic recovered: %v", rcv))
				}
			}
		}()
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
}

func (rl *defaultRateLimiter) SetDistributedClient(client RedisClient) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.redisClient = client
	rl.keyPrefix = "ratelimit" // Default prefix, could be made configurable
}
