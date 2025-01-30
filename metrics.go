package r

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Path: router.go

// Add at the top with other type definitions
type MetricsCollector interface {
	IncrementCounter(name string, tags map[string]string)
	RecordTiming(name string, duration time.Duration, tags map[string]string)
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
	key := m.formatKey(name, tags)
	value := atomic.Int64{}
	actual, _ := m.counters.LoadOrStore(key, &value)
	actual.(*atomic.Int64).Add(1)
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
		metrics[key.(string)] = value.(*atomic.Int64).Load()
		return true
	})

	// Collect values
	m.values.Range(func(key, value interface{}) bool {
		metrics[key.(string)] = value.(*atomic.Value).Load()
		return true
	})

	return metrics
}

func (m *defaultMetricsCollector) Close() error {
	return nil
}

func (m *defaultMetricsCollector) RecordValue(name string, value float64, tags map[string]string) {
	key := m.formatKey(name, tags)
	valueWrapper := &atomic.Value{}
	valueWrapper.Store(value)
	actual, loaded := m.values.LoadOrStore(key, valueWrapper)
	if loaded {
		actual.(*atomic.Value).Store(value)
	}
}

func (m *defaultMetricsCollector) formatKey(name string, tags map[string]string) string {
	if len(tags) == 0 {
		return name
	}

	// Sort tags for consistent key generation
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	sb.WriteString(name)
	for _, k := range keys {
		sb.WriteString(":")
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(tags[k])
	}
	return sb.String()
}

func (m *defaultMetricsCollector) RecordTiming(name string, duration time.Duration, tags map[string]string) {
	key := m.formatKey(name, tags)
	value := atomic.Int64{}
	actual, _ := m.timings.LoadOrStore(key, &value)
	actual.(*atomic.Int64).Store(duration.Nanoseconds())
}
