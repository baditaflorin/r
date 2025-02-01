package r

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
	"sync"
	"time"
)

// CircuitBreaker returns a middleware function that wraps a request in a circuit breaker.
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

		// Get or create a circuit breaker for this endpoint.
		cbValue, _ := breakers.LoadOrStore(key, NewCircuitBreaker(
			config.Threshold,
			config.ResetTimeout,
			m.metrics,
		))
		circuitBreaker := cbValue.(*CircuitBreaker)

		// If the circuit is open, reject the request immediately.
		if circuitBreaker.IsOpen() {
			if m.metrics != nil {
				m.metrics.IncrementCounter("circuit_breaker.rejected",
					map[string]string{"path": path, "method": method})
			}
			c.AbortWithError(http.StatusServiceUnavailable,
				fmt.Errorf("circuit breaker open for %s %s", method, path))
			return
		}

		// Begin timing the request.
		start := time.Now()

		// Create a context with timeout.
		ctx, cancel := context.WithTimeout(c, config.Timeout)
		defer cancel()
		// Replace the underlying context.
		c.(*ContextImpl).ctx = ctx

		// Execute the handler with panic recovery.
		handlerError := runHandlerWithRecovery(c, m.logger, m.metrics, path, method)
		duration := time.Since(start)

		// Update the circuit breaker based on the outcome.
		updateCircuitBreakerMetrics(m, circuitBreaker, duration, handlerError, config, path, method)

		// Record completion metrics.
		recordCompletionMetrics(m, c, duration, path, method)
	}
}

// runHandlerWithRecovery executes the request handler, recovering from panics.
// It returns any error encountered (including those due to panics).
func runHandlerWithRecovery(c Context, logger Logger, metrics MetricsCollector, path, method string) (handlerError error) {
	defer func() {
		if r := recover(); r != nil {
			handlerError = fmt.Errorf("panic: %v", r)
			if metrics != nil {
				metrics.IncrementCounter("circuit_breaker.panics", map[string]string{
					"path":   path,
					"method": method,
				})
			}
			if logger != nil {
				logger.Error("Panic in circuit breaker handler",
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
	return handlerError
}

// updateCircuitBreakerMetrics updates the circuit breaker’s state and records related metrics.
func updateCircuitBreakerMetrics(m *standardMiddleware, circuitBreaker *CircuitBreaker, duration time.Duration, handlerError error, config CircuitBreakerConfig, path, method string) {
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
			m.metrics.RecordTiming("circuit_breaker.error_latency", duration, tags)
		}
		if m.logger != nil {
			m.logger.Error("Circuit breaker failure",
				"error", handlerError,
				"duration", duration,
				"timeout", config.Timeout,
				"path", path,
				"method", method)
		}
		// Trigger immediate monitoring/update on failure.
		circuitBreaker.MonitorState()
	} else {
		circuitBreaker.RecordSuccess()
		if m.metrics != nil {
			tags := map[string]string{
				"path":   path,
				"method": method,
			}
			m.metrics.RecordTiming("circuit_breaker.success_latency", duration, tags)
			m.metrics.IncrementCounter("circuit_breaker.successes", tags)

			// Record histogram counters for various latency buckets.
			buckets := []float64{10, 50, 100, 200, 500, 1000} // in milliseconds
			for _, bucket := range buckets {
				if duration.Milliseconds() <= int64(bucket) {
					m.metrics.IncrementCounter(
						fmt.Sprintf("circuit_breaker.latency_le_%v", bucket),
						tags)
				}
			}
		}
	}
}

// recordCompletionMetrics records final metrics for the request once it has been processed.
func recordCompletionMetrics(m *standardMiddleware, c Context, duration time.Duration, path, method string) {
	if m.metrics != nil {
		m.metrics.RecordValue("circuit_breaker.request_duration_seconds",
			duration.Seconds(),
			map[string]string{
				"path":   path,
				"method": method,
				"status": fmt.Sprintf("%d", c.RequestCtx().Response.StatusCode()),
			})
		// Check if the context was cancelled.
		if c.(*ContextImpl).ctx.Err() != nil {
			m.metrics.IncrementCounter("circuit_breaker.context_cancelled",
				map[string]string{
					"path":   path,
					"method": method,
				})
		}
	}
}

// CircuitBreakerConfig holds configuration options for the circuit breaker.
type CircuitBreakerConfig struct {
	Threshold        int64
	ResetTimeout     time.Duration
	Timeout          time.Duration
	HalfOpenRequests int64
}

// CircuitBreakerOption defines a function type to set circuit breaker configuration.
type CircuitBreakerOption func(*CircuitBreakerConfig)

// defaultCircuitBreakerConfig returns the default configuration.
func defaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		Threshold:        5,
		ResetTimeout:     10 * time.Second,
		Timeout:          5 * time.Second,
		HalfOpenRequests: 2,
	}
}

// WithFailureThreshold sets a custom failure threshold.
func WithFailureThreshold(threshold int64) CircuitBreakerOption {
	return func(c *CircuitBreakerConfig) {
		c.Threshold = threshold
	}
}

// WithResetTimeout sets a custom reset timeout.
func WithResetTimeout(timeout time.Duration) CircuitBreakerOption {
	return func(c *CircuitBreakerConfig) {
		c.ResetTimeout = timeout
	}
}
