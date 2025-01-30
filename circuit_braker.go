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
	stopMonitor  chan struct{}
	doneMonitor  chan struct{}
}

const (
	StateClosed   = iota // 0: Normal operation
	StateHalfOpen        // 1: Testing the waters
	StateOpen            // 2: Circuit is broken
)

// IsOpen returns true if the circuit breaker is open (not allowing requests)
func (cb *CircuitBreaker) IsOpen() bool {
	currentState := cb.state.Load()

	// First check if we're in OPEN state and should transition to HALF-OPEN
	if currentState == StateOpen {
		lastFailureTime := cb.lastFailure.Load()
		lastFailure := time.Unix(0, lastFailureTime)

		if time.Since(lastFailure) > cb.resetTimeout {
			// Try transition to half-open
			if cb.state.CompareAndSwap(StateOpen, StateHalfOpen) {
				cb.successes.Store(0) // Reset success counter
				if cb.metrics != nil {
					cb.metrics.IncrementCounter("circuit_breaker.state_change",
						map[string]string{"from": "open", "to": "half-open"})
				}
				return false // Allow requests through in half-open state
			}
		}
		return true // Still in open state, reject requests
	}

	return false // Allow requests in HALF-OPEN or CLOSED states
}

func (cb *CircuitBreaker) GetState() int32 {
	return cb.state.Load()
}

const maxBackoff = 5 * time.Minute

func (cb *CircuitBreaker) RecordFailure() {
	failures := cb.failures.Add(1)
	cb.lastFailure.Store(time.Now().UnixNano())

	if failures >= cb.threshold {
		currentState := cb.state.Load()
		if currentState != StateOpen {
			// Transition to open state if we hit the threshold
			if cb.state.CompareAndSwap(currentState, StateOpen) {
				if cb.metrics != nil {
					cb.metrics.IncrementCounter("circuit_breaker.state_change",
						map[string]string{
							"from": func() string {
								switch currentState {
								case StateClosed:
									return "closed"
								case StateHalfOpen:
									return "half-open"
								default:
									return "unknown"
								}
							}(),
							"to": "open",
						})
				}
			}
		}
	}

	if cb.metrics != nil {
		cb.metrics.IncrementCounter("circuit_breaker.failure", nil)
	}
}

func (cb *CircuitBreaker) RecordSuccess() {
	currentState := cb.state.Load()

	switch currentState {
	case StateHalfOpen:
		newSuccesses := cb.successes.Add(1)
		if newSuccesses >= cb.halfOpenMax {
			// Attempt transition to closed state
			if cb.state.CompareAndSwap(StateHalfOpen, StateClosed) {
				cb.failures.Store(0)  // Reset failure counter
				cb.successes.Store(0) // Reset success counter
				if cb.metrics != nil {
					cb.metrics.IncrementCounter("circuit_breaker.state_change",
						map[string]string{"from": "half-open", "to": "closed"})
				}
			}
		}

	case StateClosed:
		// In closed state, just reset failure counter on success
		cb.failures.Store(0)

	case StateOpen:
		// This shouldn't happen normally
		if cb.metrics != nil {
			cb.metrics.IncrementCounter("circuit_breaker.unexpected_success",
				map[string]string{"state": "open"})
		}
	}

	if cb.metrics != nil {
		cb.metrics.IncrementCounter("circuit_breaker.success",
			map[string]string{"state": stateToString(currentState)})
	}
}

func stateToString(state int32) string {
	switch state {
	case StateClosed:
		return "closed"
	case StateHalfOpen:
		return "half-open"
	case StateOpen:
		return "open"
	default:
		return "unknown"
	}
}
func NewCircuitBreaker(threshold int64, resetTimeout time.Duration, metrics MetricsCollector) *CircuitBreaker {
	cb := &CircuitBreaker{
		threshold:    threshold,
		resetTimeout: resetTimeout,
		metrics:      metrics,
		halfOpenMax:  2,
		stopMonitor:  make(chan struct{}),
		doneMonitor:  make(chan struct{}),
	}

	// Start in closed state
	cb.state.Store(StateClosed)
	go cb.MonitorLoop()

	return cb
}

func (cb *CircuitBreaker) MonitorState() {
	if cb.metrics == nil {
		return
	}

	status := cb.GetStatus()
	cb.metrics.RecordValue("circuit_breaker.error_rate", status.ErrorPercentage, nil)
	cb.metrics.RecordValue("circuit_breaker.failures", float64(status.Failures), nil)
}

func (cb *CircuitBreaker) MonitorLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer func() {
		ticker.Stop()
		close(cb.doneMonitor)
	}()

	for {
		select {
		case <-ticker.C:
			cb.MonitorState()
		case <-cb.stopMonitor:
			return
		}
	}
}

type CircuitBreakerStatus struct {
	State           string    `json:"state"`
	Failures        int64     `json:"failures"`
	LastFailure     time.Time `json:"last_failure"`
	SuccessStreak   int64     `json:"success_streak"`
	TotalRequests   int64     `json:"total_requests"`
	ErrorPercentage float64   `json:"error_percentage"`
}

func (cb *CircuitBreaker) GetStatus() CircuitBreakerStatus {
	currentState := cb.state.Load()
	var stateName string
	switch currentState {
	case StateOpen:
		stateName = "OPEN"
	case StateHalfOpen:
		stateName = "HALF-OPEN"
	case StateClosed:
		stateName = "CLOSED"
	}

	failures := cb.failures.Load()
	successes := cb.successes.Load()
	total := failures + successes
	errorRate := 0.0
	if total > 0 {
		errorRate = float64(failures) / float64(total) * 100
	}

	return CircuitBreakerStatus{
		State:           stateName,
		Failures:        failures,
		LastFailure:     time.Unix(0, cb.lastFailure.Load()),
		SuccessStreak:   cb.successes.Load(),
		TotalRequests:   total,
		ErrorPercentage: errorRate,
	}
}

func (cb *CircuitBreaker) Close() error {
	close(cb.stopMonitor)
	<-cb.doneMonitor
	return nil
}
