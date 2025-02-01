// File: route_metrics.go
package r

import (
	"time"
)

// UpdateRouteMetrics updates metrics for the route identified by method and path.
// It increments call counts, error counts (if an error is passed), and updates
// the average latency using an exponential moving average.
// The function is modularized into helper functions for clarity and testability.
func UpdateRouteMetrics(router *RouterImpl, method, path string, duration time.Duration, err error) {
	// If no metrics collector is configured, skip metrics update.
	if router.routeMetrics == nil {
		return
	}

	routeKey := method + path
	router.routesMu.RLock()
	route, exists := router.routes[routeKey]
	router.routesMu.RUnlock()

	if !exists {
		return
	}

	incrementRouteCounters(route, duration, err)
}

// incrementRouteCounters updates the total call count, last called time,
// and, if an error occurred, increments the error count.
func incrementRouteCounters(route *Route, duration time.Duration, err error) {
	route.totalCalls.Add(1)
	route.lastCalled.Store(time.Now().UnixNano())

	if err != nil {
		route.errorCount.Add(1)
	}

	updateAverageLatency(route, duration)
}

// updateAverageLatency updates the route's average latency using an exponential moving average.
func updateAverageLatency(route *Route, duration time.Duration) {
	currentAvg := float64(route.avgLatency.Load())
	// Calculate new average: 90% of the current average + 10% of the new duration.
	newAvg := (currentAvg*0.9 + float64(duration.Nanoseconds())*0.1)
	route.avgLatency.Store(int64(newAvg))
}
