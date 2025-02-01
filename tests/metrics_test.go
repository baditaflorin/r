package r_test

import (
	"github.com/baditaflorin/r"
	"sync"
	"testing"
	"time"
)

func TestMetricsCollector_CounterOperations(t *testing.T) {
	mc := r.NewDefaultMetricsCollector()

	// Test counter increments
	tags := map[string]string{"test": "value"}
	for i := 0; i < 5; i++ {
		mc.IncrementCounter("test.counter", tags)
	}

	metrics := mc.GetMetrics()
	counter, ok := metrics["test.counter:test=value"]
	if !ok {
		t.Fatal("Counter not found in metrics")
	}

	if counter != int64(5) {
		t.Errorf("Expected counter value 5, got %v", counter)
	}
}

func TestMetricsCollector_TimingOperations(t *testing.T) {
	mc := r.NewDefaultMetricsCollector()
	tags := map[string]string{"operation": "test"}

	// Record some timings
	durations := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		300 * time.Millisecond,
	}

	for _, d := range durations {
		mc.RecordTiming("test.timing", d, tags)
	}

	metrics := mc.GetMetrics()
	timing, ok := metrics["test.timing:operation=test"]
	if !ok {
		t.Fatal("Timing metric not found")
	}

	// The last recorded value should be stored
	if timing.(int64) != durations[len(durations)-1].Nanoseconds() {
		t.Errorf("Expected timing value %v, got %v", durations[len(durations)-1].Nanoseconds(), timing)
	}
}

func TestMetricsCollector_ValueOperations(t *testing.T) {
	mc := r.NewDefaultMetricsCollector()

	testCases := []struct {
		name  string
		value float64
		tags  map[string]string
	}{
		{
			name:  "test.gauge1",
			value: 42.5,
			tags:  map[string]string{"type": "gauge"},
		},
		{
			name:  "test.gauge2",
			value: -17.8,
			tags:  map[string]string{"type": "temperature"},
		},
	}

	for _, tc := range testCases {
		mc.RecordValue(tc.name, tc.value, tc.tags)

		metrics := mc.GetMetrics()
		key := tc.name
		for k, v := range tc.tags {
			key += ":" + k + "=" + v
		}

		if value, ok := metrics[key]; !ok {
			t.Errorf("Value not found for metric %s", key)
		} else if value != tc.value {
			t.Errorf("Expected value %v, got %v for metric %s", tc.value, value, key)
		}
	}
}

func TestMetricsCollector_Concurrency(t *testing.T) {
	mc := r.NewDefaultMetricsCollector()

	const numGoroutines = 100
	const numIncrements = 1000

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < numIncrements; j++ {
				mc.IncrementCounter("test.concurrent.counter", nil)
			}
		}()
	}

	wg.Wait()

	metrics := mc.GetMetrics()
	count := metrics["test.concurrent.counter"]

	if count != int64(numGoroutines*numIncrements) {
		t.Errorf("Expected count %d, got %d", numGoroutines*numIncrements, count)
	}
}

func TestMetricsCollector_TimingAggregation(t *testing.T) {
	mc := r.NewDefaultMetricsCollector()

	durations := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		300 * time.Millisecond,
	}

	tags := map[string]string{"operation": "test"}

	for _, d := range durations {
		mc.RecordTiming("test.timing.aggregated", d, tags)
	}

	metrics := mc.GetMetrics()
	timing := metrics["test.timing.aggregated:operation=test"].(int64)

	expectedNanos := durations[len(durations)-1].Nanoseconds()
	if timing != expectedNanos {
		t.Errorf("Expected timing %d, got %d", expectedNanos, timing)
	}
}
