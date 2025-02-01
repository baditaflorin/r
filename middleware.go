// middleware.go
package r

import (
	"bytes"
	"context"
	"fmt"
	"github.com/google/uuid"
	"net/http"
	"os"
	"runtime/debug"
	"sync"
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
	CircuitBreaker(opts ...CircuitBreakerOption) MiddlewareFunc
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

type MiddlewareOption func(*standardMiddleware)

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
		// Use a long-lived context (for example, context.Background()).
		m.rateLimiter = NewDefaultRateLimiter(context.Background(), reqs, per)
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
