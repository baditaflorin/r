// File: tests/router_panic_test.go
package r_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/baditaflorin/r"
)

func TestRouter_PanicHandling(t *testing.T) {
	router := r.NewRouter()
	panicMessage := "intentional panic"

	// Set a custom panic handler that responds with a JSON error message.
	router.PanicHandler(func(c r.Context, rcv interface{}) {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("%v", rcv),
		})
	})

	// Define a route that intentionally panics.
	router.GET("/panic", func(c r.Context) {
		panic(panicMessage)
	})

	// Create a test context for the /panic route.
	ctx := createTestContext()
	ctx.RequestCtx().Request.Header.SetMethod("GET")
	ctx.RequestCtx().Request.SetRequestURI("/panic")
	router.ServeHTTP(ctx.RequestCtx())

	// Expect a 500 status code.
	if ctx.RequestCtx().Response.StatusCode() != http.StatusInternalServerError {
		t.Errorf("Expected status code 500 for panic, got %d", ctx.RequestCtx().Response.StatusCode())
	}

	// Verify that the JSON response contains the expected error message.
	var response map[string]string
	if err := json.Unmarshal(ctx.RequestCtx().Response.Body(), &response); err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}
	if response["error"] != panicMessage {
		t.Errorf("Expected error message '%s', got '%s'", panicMessage, response["error"])
	}
}
