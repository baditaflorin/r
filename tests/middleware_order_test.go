// File: tests/middleware_order_test.go
package r_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/baditaflorin/r"
)

func TestMiddleware_Order(t *testing.T) {
	router := r.NewRouter()

	// First middleware sets an initial value.
	router.Use(func(c r.Context) {
		c.Set("order", "first")
		c.Next()
	})

	// Second middleware appends to the value.
	router.Use(func(c r.Context) {
		if val, ok := c.Get("order"); ok {
			c.Set("order", fmt.Sprintf("%s-second", val))
		} else {
			t.Error("Expected 'order' key to be set by previous middleware")
		}
		c.Next()
	})

	// The handler returns the final value.
	router.GET("/order", func(c r.Context) {
		if val, ok := c.Get("order"); ok {
			c.JSON(200, map[string]string{"order": val.(string)})
		} else {
			c.JSON(500, map[string]string{"error": "order key not found"})
		}
	})

	// Create a test context simulating a GET request.
	ctx := CreateTestContext()
	ctx.RequestCtx().Request.Header.SetMethod("GET")
	ctx.RequestCtx().Request.SetRequestURI("/order")

	router.ServeHTTP(ctx.RequestCtx())

	var resp map[string]string
	if err := json.Unmarshal(ctx.RequestCtx().Response.Body(), &resp); err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}

	expected := "first-second"
	if resp["order"] != expected {
		t.Errorf("Expected order '%s', got '%s'", expected, resp["order"])
	}
}
