// File: tests/router_method_not_allowed_test.go
package r_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/baditaflorin/r"
)

func TestRouter_MethodNotAllowed(t *testing.T) {
	router := r.NewRouter()

	// Set up a custom method-not-allowed handler.
	router.MethodNotAllowed(func(c r.Context) {
		c.JSON(405, map[string]string{"error": "method not allowed"})
	})

	// Register a route only for GET.
	router.GET("/test", func(c r.Context) {
		c.JSON(200, map[string]string{"message": "ok"})
	})

	// Create a test context simulating a POST request (which is not allowed).
	ctx := CreateTestContext()
	ctx.RequestCtx().Request.Header.SetMethod("POST")
	ctx.RequestCtx().Request.SetRequestURI("/test")

	router.ServeHTTP(ctx.RequestCtx())

	// Check that the status code is 405.
	if ctx.RequestCtx().Response.StatusCode() != http.StatusMethodNotAllowed {
		t.Errorf("Expected status code 405, got %d", ctx.RequestCtx().Response.StatusCode())
	}

	// Verify that the JSON error message is correct.
	var resp map[string]string
	if err := json.Unmarshal(ctx.RequestCtx().Response.Body(), &resp); err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}
	if resp["error"] != "method not allowed" {
		t.Errorf("Expected error message 'method not allowed', got '%s'", resp["error"])
	}
}
