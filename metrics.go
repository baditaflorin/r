package r

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Path: router.go

// Add at the top with other type definitions
type MetricsCollector interface {
	IncrementCounter(name string, tags map[string]string)
	RecordTiming(name string, duration time.Duration)
	RecordValue(name string, value float64, tags map[string]string)
	GetMetrics() map[string]interface{}
	Close() error
}

// Add a simple default implementation
type defaultMetricsCollector struct {
	counters sync.Map
	timings  sync.Map
	values   sync.Map // Add values map for RecordValue
}

func NewDefaultMetricsCollector() MetricsCollector {
	return &defaultMetricsCollector{}
}

func (m *defaultMetricsCollector) IncrementCounter(name string, tags map[string]string) {
	key := name
	for k, v := range tags {
		key += fmt.Sprintf(":%s=%s", k, v)
	}
	value := atomic.Int64{}
	actual, _ := m.counters.LoadOrStore(key, &value)
	actual.(*atomic.Int64).Add(1)
}

func (m *defaultMetricsCollector) RecordTiming(name string, duration time.Duration) {
	value := atomic.Int64{}
	actual, _ := m.timings.LoadOrStore(name, &value)
	actual.(*atomic.Int64).Store(duration.Nanoseconds())
}

func (m *defaultMetricsCollector) GetMetrics() map[string]interface{} {
	metrics := make(map[string]interface{})

	// Collect counters
	m.counters.Range(func(key, value interface{}) bool {
		metrics[key.(string)] = value.(*atomic.Int64).Load()
		return true
	})

	// Collect timings
	m.timings.Range(func(key, value interface{}) bool {
		metrics["timing_"+key.(string)] = value.(*atomic.Int64).Load()
		return true
	})

	// Collect values
	m.values.Range(func(key, value interface{}) bool {
		metrics["value_"+key.(string)] = value.(*atomic.Value).Load()
		return true
	})

	return metrics
}

func (m *defaultMetricsCollector) Close() error {
	// Perform any cleanup needed
	return nil
}

func (m *defaultMetricsCollector) RecordValue(name string, value float64, tags map[string]string) {
	key := name
	for k, v := range tags {
		key += fmt.Sprintf(":%s=%s", k, v)
	}

	// Store the value using atomic operations
	valueWrapper := &atomic.Value{}
	valueWrapper.Store(value)

	actual, loaded := m.values.LoadOrStore(key, valueWrapper)
	if loaded {
		actual.(*atomic.Value).Store(value)
	}
}
