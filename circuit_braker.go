package r

import (
	"sync/atomic"
	"time"
)

// CircuitBreaker represents a stateful circuit breaker.
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

// Circuit breaker states.
const (
	StateClosed   = iota // 0: Normal operation
	StateHalfOpen        // 1: Testing the waters
	StateOpen            // 2: Circuit is broken
)

// IsOpen returns true if the circuit breaker is open.
// When in the open state, it may try to transition to half-open if the reset timeout has elapsed.
func (cb *CircuitBreaker) IsOpen() bool {
	currentState := cb.state.Load()

	if currentState == StateOpen {
		if cb.isResetTimeoutElapsed() {
			cb.tryTransitionFromOpenToHalfOpen()
			return false // Allow requests in half-open state.
		}
		return true // Still open.
	}

	return false // Allow requests in closed or half-open states.
}

// isResetTimeoutElapsed checks whether the reset timeout has passed since the last failure.
func (cb *CircuitBreaker) isResetTimeoutElapsed() bool {
	lastFailureTime := cb.lastFailure.Load()
	return time.Since(time.Unix(0, lastFailureTime)) > cb.resetTimeout
}

// tryTransitionFromOpenToHalfOpen attempts to move from open to half-open state.
func (cb *CircuitBreaker) tryTransitionFromOpenToHalfOpen() {
	if cb.state.CompareAndSwap(StateOpen, StateHalfOpen) {
		cb.resetSuccesses()
		cb.logStateChange("open", "half-open")
	}
}

// GetState returns the current state.
func (cb *CircuitBreaker) GetState() int32 {
	return cb.state.Load()
}

// RecordFailure increments the failure counter and, if the threshold is reached,
// transitions the breaker to the open state.
func (cb *CircuitBreaker) RecordFailure() {
	failures := cb.failures.Add(1)
	cb.lastFailure.Store(time.Now().UnixNano())
	cb.checkThresholdAndTransition(failures)
	cb.incrementFailureMetric()
}

// checkThresholdAndTransition transitions to open state if failures exceed threshold.
func (cb *CircuitBreaker) checkThresholdAndTransition(failures int64) {
	if failures >= cb.threshold {
		currentState := cb.state.Load()
		if currentState != StateOpen {
			if cb.state.CompareAndSwap(currentState, StateOpen) {
				cb.logStateChange(stateToString(currentState), "open")
			}
		}
	}
}

// incrementFailureMetric records a failure in the metrics collector.
func (cb *CircuitBreaker) incrementFailureMetric() {
	if cb.metrics != nil {
		cb.metrics.IncrementCounter("circuit_breaker.failure", nil)
	}
}

// RecordSuccess handles a successful request by updating counters and, if in half-open state,
// possibly transitioning to the closed state.
func (cb *CircuitBreaker) RecordSuccess() {
	currentState := cb.state.Load()
	switch currentState {
	case StateHalfOpen:
		cb.handleHalfOpenSuccess()
	case StateClosed:
		cb.resetFailures() // Reset failures on a successful request.
	case StateOpen:
		cb.logUnexpectedSuccess()
	}
	cb.incrementSuccessMetric(currentState)
}

// handleHalfOpenSuccess increments the success counter and transitions to closed
// if the success count reaches the configured maximum.
func (cb *CircuitBreaker) handleHalfOpenSuccess() {
	newSuccesses := cb.successes.Add(1)
	if newSuccesses >= cb.halfOpenMax {
		if cb.state.CompareAndSwap(StateHalfOpen, StateClosed) {
			cb.resetFailures()
			cb.resetSuccesses()
			cb.logStateChange("half-open", "closed")
		}
	}
}

// resetFailures resets the failure counter.
func (cb *CircuitBreaker) resetFailures() {
	cb.failures.Store(0)
}

// resetSuccesses resets the success counter.
func (cb *CircuitBreaker) resetSuccesses() {
	cb.successes.Store(0)
}

// logUnexpectedSuccess logs an unexpected success when in the open state.
func (cb *CircuitBreaker) logUnexpectedSuccess() {
	if cb.metrics != nil {
		cb.metrics.IncrementCounter("circuit_breaker.unexpected_success",
			map[string]string{"state": "open"})
	}
}

// incrementSuccessMetric records a success metric.
func (cb *CircuitBreaker) incrementSuccessMetric(currentState int32) {
	if cb.metrics != nil {
		cb.metrics.IncrementCounter("circuit_breaker.success",
			map[string]string{"state": stateToString(currentState)})
	}
}

// stateToString converts a state constant to a string.
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

// logStateChange records a state change event in the metrics collector.
func (cb *CircuitBreaker) logStateChange(from, to string) {
	if cb.metrics != nil {
		cb.metrics.IncrementCounter("circuit_breaker.state_change",
			map[string]string{"from": from, "to": to})
	}
}

// NewCircuitBreaker constructs a new CircuitBreaker, starts its monitoring loop, and returns it.
func NewCircuitBreaker(threshold int64, resetTimeout time.Duration, metrics MetricsCollector) *CircuitBreaker {
	cb := &CircuitBreaker{
		threshold:    threshold,
		resetTimeout: resetTimeout,
		metrics:      metrics,
		halfOpenMax:  2,
		stopMonitor:  make(chan struct{}),
		doneMonitor:  make(chan struct{}),
	}
	cb.state.Store(StateClosed)
	go cb.monitorLoop()
	return cb
}

// MonitorState records current error rate and failure counts.
func (cb *CircuitBreaker) MonitorState() {
	if cb.metrics == nil {
		return
	}
	status := cb.GetStatus()
	cb.metrics.RecordValue("circuit_breaker.error_rate", status.ErrorPercentage, nil)
	cb.metrics.RecordValue("circuit_breaker.failures", float64(status.Failures), nil)
}

// monitorLoop periodically calls MonitorState until stopped.
func (cb *CircuitBreaker) monitorLoop() {
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

// CircuitBreakerStatus contains a snapshot of the circuit breaker's metrics.
type CircuitBreakerStatus struct {
	State           string    `json:"state"`
	Failures        int64     `json:"failures"`
	LastFailure     time.Time `json:"last_failure"`
	SuccessStreak   int64     `json:"success_streak"`
	TotalRequests   int64     `json:"total_requests"`
	ErrorPercentage float64   `json:"error_percentage"`
}

// GetStatus returns a snapshot of the circuit breaker's current status.
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
	default:
		stateName = "UNKNOWN"
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
		SuccessStreak:   successes,
		TotalRequests:   total,
		ErrorPercentage: errorRate,
	}
}

// Close stops the monitor loop and cleans up resources.
func (cb *CircuitBreaker) Close() error {
	close(cb.stopMonitor)
	<-cb.doneMonitor
	return nil
}
