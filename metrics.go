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
	GetMetrics() map[string]interface{}
}

// Add a simple default implementation
type defaultMetricsCollector struct {
	counters sync.Map
	timings  sync.Map
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

	m.counters.Range(func(key, value interface{}) bool {
		metrics[key.(string)] = value.(*atomic.Int64).Load()
		return true
	})

	m.timings.Range(func(key, value interface{}) bool {
		metrics["timing_"+key.(string)] = value.(*atomic.Int64).Load()
		return true
	})

	return metrics
}
