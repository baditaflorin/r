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
	stopMonitor  chan struct{} // New field for stopping the monitor
}

const (
	stateOpen = iota
	stateHalfOpen
	stateClosed
)

func (cb *CircuitBreaker) isOpen() bool {
	currentState := cb.getState()
	if currentState == stateOpen {
		lastFailure := time.Unix(0, cb.lastFailure.Load())
		if time.Since(lastFailure) > cb.resetTimeout {
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
		if cb.successes.Load() >= cb.halfOpenMax {
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

// Atomic getter for state
func (cb *CircuitBreaker) getState() int32 {
	return cb.state.Load()
}

// Atomic setter for state
func (cb *CircuitBreaker) setState(newState int32) {
	cb.state.Store(newState)
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
			// Trigger immediate state monitoring on transition
			cb.monitorState()
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
	cb := &CircuitBreaker{
		threshold:    threshold,
		resetTimeout: resetTimeout,
		metrics:      metrics,
		halfOpenMax:  2,
		stopMonitor:  make(chan struct{}),
	}

	// Start a single monitoring loop
	go cb.monitorLoop()

	return cb
}

// Run monitorState at a fixed interval instead of triggering multiple times
func (cb *CircuitBreaker) monitorLoop() {
	ticker := time.NewTicker(time.Second * 15) // Monitor every 15 seconds
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cb.monitorState()
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
	case stateOpen:
		stateName = "OPEN"
	case stateHalfOpen:
		stateName = "HALF-OPEN"
	case stateClosed:
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

func (cb *CircuitBreaker) monitorState() {
	if cb.metrics == nil {
		return
	}

	status := cb.GetStatus()
	cb.metrics.RecordValue("circuit_breaker.error_rate", status.ErrorPercentage, nil)
	cb.metrics.RecordValue("circuit_breaker.total_requests", float64(status.TotalRequests), nil)
	cb.metrics.RecordValue("circuit_breaker.failures", float64(status.Failures), nil)

	stateMetric := 0.0
	switch status.State {
	case "OPEN":
		stateMetric = 2.0
	case "HALF-OPEN":
		stateMetric = 1.0
	case "CLOSED":
		stateMetric = 0.0
	}
	cb.metrics.RecordValue("circuit_breaker.state", stateMetric, nil)
}

func (cb *CircuitBreaker) startStateMonitor() {
	ticker := time.NewTicker(time.Second * 15) // Monitor every 15 seconds
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cb.monitorState()
		case <-cb.stopMonitor:
			return
		}
	}
}

func (cb *CircuitBreaker) Close() error {
	close(cb.stopMonitor)
	return nil
}
