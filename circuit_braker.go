package r

import (
	"sync"
	"sync/atomic"
	"time"
)

type CircuitBreaker struct {
	failures     atomic.Int64
	lastFailure  atomic.Int64
	threshold    int64
	resetTimeout time.Duration
	mu           sync.RWMutex
}

func (cb *CircuitBreaker) isOpen() bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	failures := cb.failures.Load()
	if failures >= cb.threshold {
		lastFailure := time.Unix(0, cb.lastFailure.Load())
		if time.Since(lastFailure) > cb.resetTimeout {
			// Circuit breaker auto-reset after timeout
			cb.failures.Store(0)
			return false
		}
		return true
	}
	return false
}

func (cb *CircuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures.Add(1)
	cb.lastFailure.Store(time.Now().UnixNano())
}

// Add constructor for better encapsulation
func NewCircuitBreaker(threshold int64, resetTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		threshold:    threshold,
		resetTimeout: resetTimeout,
	}
}
