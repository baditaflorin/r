// File: tests/router_custom_not_found_test.go
package r_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/baditaflorin/r"
)

func TestRouter_CustomNotFoundHandler(t *testing.T) {
	router := r.NewRouter()

	// Set a custom not found handler.
	router.NotFound(func(c r.Context) {
		c.JSON(404, map[string]string{"error": "custom not found"})
	})

	// Create a test context for a non-existent route.
	ctx := CreateTestContext()
	ctx.RequestCtx().Request.Header.SetMethod("GET")
	ctx.RequestCtx().Request.SetRequestURI("/non-existent")

	router.ServeHTTP(ctx.RequestCtx())

	if ctx.RequestCtx().Response.StatusCode() != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", ctx.RequestCtx().Response.StatusCode())
	}

	var resp map[string]string
	if err := json.Unmarshal(ctx.RequestCtx().Response.Body(), &resp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}
	if resp["error"] != "custom not found" {
		t.Errorf("Expected error message 'custom not found', got '%s'", resp["error"])
	}
}
