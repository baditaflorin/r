package r_test

import (
	"github.com/baditaflorin/r"
	"testing"
	"time"
)

func TestCircuitBreaker_OpenState(t *testing.T) {
	cb := r.NewCircuitBreaker(3, 2*time.Second, nil)
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordFailure()
	if !cb.IsOpen() {
		t.Errorf("Expected circuit breaker to be open")
	}
}

func TestCircuitBreaker_Transitions(t *testing.T) {
	cb := r.NewCircuitBreaker(2, 1*time.Second, nil)
	cb.RecordFailure()
	cb.RecordFailure()
	if !cb.IsOpen() {
		t.Errorf("Expected circuit breaker to be open")
	}

	// Wait a bit longer to ensure state transition
	time.Sleep(1100 * time.Millisecond) // Give extra time buffer

	if cb.GetState() != r.StateHalfOpen {
		t.Errorf("Expected circuit breaker to transition to half-open, but got %v", cb.GetState())
	}
}

func TestCircuitBreaker_ResetOnSuccess(t *testing.T) {
	cb := r.NewCircuitBreaker(2, 1*time.Second, nil)
	cb.RecordFailure()
	cb.RecordFailure()

	time.Sleep(1100 * time.Millisecond) // Ensure transition time
	cb.RecordSuccess()

	// Debugging output
	t.Logf("Circuit breaker state: %d", cb.GetState())

	if cb.GetState() != r.StateClosed {
		t.Errorf("Expected circuit breaker to transition to closed after success")
	}
}

func TestCircuitBreaker_HandlesRapidFailures(t *testing.T) {
	cb := r.NewCircuitBreaker(5, 500*time.Millisecond, nil)
	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}
	if !cb.IsOpen() {
		t.Errorf("Circuit breaker should be open after rapid failures")
	}
}
