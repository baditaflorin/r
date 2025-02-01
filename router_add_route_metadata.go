package r

import (
	"fmt"
	"time"
)

// addRouteMetadata registers a new route by creating a Route, validating it,
// recording metrics, and storing it in the router's map.
func (r *RouterImpl) addRouteMetadata(method, path string, handlers ...HandlerFunc) {
	r.initializeIfNeeded()

	// Lock the routes map during updates.
	r.routesMu.Lock()
	defer r.routesMu.Unlock()

	// Create the route metadata.
	route := newRoute(method, path)

	// Validate the route; if invalid, panic via the configured handler and return.
	if !r.validateRoute(route) {
		return
	}

	// Record a metric for the route registration.
	r.recordRouteRegistrationMetrics(method, path)

	// Store the route using a composite key.
	r.storeRoute(route)
}

// newRoute creates a new Route instance with the current timestamp.
func newRoute(method, path string) *Route {
	return &Route{
		Path:   path,
		Method: method,
		Added:  time.Now(),
	}
}

// validateRoute checks whether the route meets the criteria defined by the validator.
// If the route is invalid, it invokes the panic handler (if one is set) and returns false.
func (r *RouterImpl) validateRoute(route *Route) bool {
	if r.validator != nil {
		if err := r.validator.ValidateRoute(route.Path, route.Method); err != nil {
			r.panicHandler(nil, fmt.Sprintf("invalid route %s %s: %v", route.Method, route.Path, err))
			return false
		}
	}
	return true
}

// recordRouteRegistrationMetrics increments a metric for the registered route.
func (r *RouterImpl) recordRouteRegistrationMetrics(method, path string) {
	if r.routeMetrics != nil {
		r.routeMetrics.IncrementCounter("route.registered", map[string]string{
			"method": method,
			"path":   path,
		})
	}
}

// storeRoute saves the route in the routes map using a composite key.
func (r *RouterImpl) storeRoute(route *Route) {
	routeKey := route.Method + route.Path
	r.routes[routeKey] = route
}
