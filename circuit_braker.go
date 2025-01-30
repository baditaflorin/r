package r

import (
	"sync/atomic"
	"time"
)

type CircuitBreaker struct {
	failures     atomic.Int64
	successes    atomic.Int64
	lastFailure  atomic.Int64
	threshold    int64
	resetTimeout time.Duration
	halfOpenMax  int64
	metrics      MetricsCollector
	state        atomic.Int32
}

const (
	stateOpen = iota
	stateHalfOpen
	stateClosed
)

func (cb *CircuitBreaker) isOpen() bool {
	currentState := cb.state.Load()
	if currentState == stateOpen {
		lastFailure := time.Unix(0, cb.lastFailure.Load())
		if time.Since(lastFailure) > cb.resetTimeout {
			// Try transitioning to half-open state
			if cb.state.CompareAndSwap(stateOpen, stateHalfOpen) {
				cb.successes.Store(0)
				if cb.metrics != nil {
					cb.metrics.IncrementCounter("circuit_breaker.state_change",
						map[string]string{"from": "open", "to": "half-open"})
				}
			}
			return false
		}
		return true
	}

	if currentState == stateHalfOpen {
		// Only allow halfOpenMax requests in half-open state
		currentSuccesses := cb.successes.Load()
		if currentSuccesses >= cb.halfOpenMax {
			if cb.state.CompareAndSwap(stateHalfOpen, stateClosed) {
				cb.failures.Store(0)
				if cb.metrics != nil {
					cb.metrics.IncrementCounter("circuit_breaker.state_change",
						map[string]string{"from": "half-open", "to": "closed"})
				}
			}
		}
		return false
	}

	return false
}

func (cb *CircuitBreaker) recordFailure() {
	failures := cb.failures.Add(1)
	cb.lastFailure.Store(time.Now().UnixNano())

	if cb.metrics != nil {
		cb.metrics.IncrementCounter("circuit_breaker.failure", nil)
	}

	if failures >= cb.threshold {
		if cb.state.CompareAndSwap(stateClosed, stateOpen) ||
			cb.state.CompareAndSwap(stateHalfOpen, stateOpen) {
			if cb.metrics != nil {
				cb.metrics.IncrementCounter("circuit_breaker.state_change",
					map[string]string{"to": "open"})
			}
		}
	}
}

func (cb *CircuitBreaker) recordSuccess() {
	if cb.state.Load() == stateHalfOpen {
		cb.successes.Add(1)
	}

	if cb.metrics != nil {
		cb.metrics.IncrementCounter("circuit_breaker.success", nil)
	}
}

// Add constructor for better encapsulation
func NewCircuitBreaker(threshold int64, resetTimeout time.Duration, metrics MetricsCollector) *CircuitBreaker {
	return &CircuitBreaker{
		threshold:    threshold,
		resetTimeout: resetTimeout,
		metrics:      metrics,
		halfOpenMax:  2, // Default value for half-open state max requests
	}
}
