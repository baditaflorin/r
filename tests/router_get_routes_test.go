// File: tests/router_get_routes_test.go
package r_test

import (
	"testing"

	"github.com/baditaflorin/r"
)

// TestRouter_GetRoutes verifies that registered routes are correctly tracked.
func TestRouter_GetRoutes(t *testing.T) {
	router := r.NewRouter()

	// Register a couple of routes.
	router.GET("/foo", func(c r.Context) {
		c.String(200, "foo")
	})
	router.POST("/bar", func(c r.Context) {
		c.String(200, "bar")
	})

	// Retrieve the registered routes.
	routes := router.(*r.RouterImpl).GetRoutes()
	if len(routes) < 2 {
		t.Errorf("Expected at least 2 routes, got %d", len(routes))
	}

	// Check that each expected route appears in the list.
	routeFound := map[string]bool{
		"GET/foo":  false,
		"POST/bar": false,
	}
	for _, rt := range routes {
		key := rt.Method + rt.Path
		if _, exists := routeFound[key]; exists {
			routeFound[key] = true
		}
	}

	for key, found := range routeFound {
		if !found {
			t.Errorf("Expected route '%s' was not found in the registered routes", key)
		}
	}
}
