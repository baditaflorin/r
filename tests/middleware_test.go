// File: tests/middleware_test.go
package r_test

import (
	"github.com/baditaflorin/r"
	"github.com/valyala/fasthttp"
	"testing"
	"time"
)

func TestMiddleware_RateLimit(t *testing.T) {
	router := r.NewRouter()

	// Create middleware provider
	provider := r.NewMiddlewareProvider(router.(*r.RouterImpl))

	// Add rate limiting - 3 requests per 2 seconds to be more forgiving
	router.Use(provider.RateLimit(3, 2*time.Second))

	// Add test route
	router.GET("/test", func(c r.Context) {
		c.JSON(200, map[string]string{"status": "ok"})
	})

	// Create test server
	server := r.NewServer(r.Config{
		Handler: router,
	})

	// Helper function to make request and get status
	makeRequest := func() int {
		ctx := createTestContext()
		ctx.RequestCtx().Request.Header.SetMethod("GET")
		ctx.RequestCtx().Request.SetRequestURI("/test")
		ctx.RequestCtx().Request.Header.Set("X-Real-IP", "127.0.0.1")

		server.ServeHTTP(ctx.RequestCtx())

		status := ctx.RequestCtx().Response.StatusCode()
		t.Logf("Response status: %d, Body: %s",
			status,
			string(ctx.RequestCtx().Response.Body()))
		return status
	}

	// Test first three requests with longer delays
	for i := 0; i < 2; i++ {
		t.Logf("Making request %d", i+1)
		status := makeRequest()
		if status != 200 {
			t.Errorf("Request %d: expected status 200, got %d", i+1, status)
		}
		time.Sleep(200 * time.Millisecond) // Much longer delay between requests
	}

	// Make several requests that should be rate limited
	t.Log("Making requests that should be rate limited")
	rateLimitedCount := 0
	for i := 0; i < 3; i++ {
		if makeRequest() == 429 {
			rateLimitedCount++
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Check that at least one request was rate limited
	if rateLimitedCount == 0 {
		t.Error("Expected at least one request to be rate limited")
	}

	// Wait for rate limit to reset
	t.Log("Waiting for rate limit to reset...")
	time.Sleep(2 * time.Second)

	// After reset, should be able to make requests again
	t.Log("Making request after reset")
	if status := makeRequest(); status != 200 {
		t.Errorf("Expected status 200 after reset, got %d", status)
	}
}

func TestMiddleware_RequestID(t *testing.T) {
	router := r.NewRouter()
	provider := r.NewMiddlewareProvider(router.(*r.RouterImpl))

	router.Use(provider.RequestID())

	var capturedRequestID string
	router.GET("/test", func(c r.Context) {
		capturedRequestID = c.RequestID()
		c.JSON(200, map[string]string{"request_id": c.RequestID()})
	})

	// Create test context
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/test")

	// Create test server and make request
	server := r.NewServer(r.Config{
		Handler: router,
	})
	server.ServeHTTP(ctx)

	// Verify response headers
	requestID := string(ctx.Response.Header.Peek("X-Request-ID"))
	if requestID == "" {
		t.Error("X-Request-ID header not set")
	}

	if requestID != capturedRequestID {
		t.Errorf("Request ID mismatch: header %s vs context %s",
			requestID, capturedRequestID)
	}
}

func TestMiddleware_CORS(t *testing.T) {
	router := r.NewRouter()
	provider := r.NewMiddlewareProvider(router.(*r.RouterImpl))

	// Add debug endpoint
	router.OPTIONS("/test", func(c r.Context) {
		t.Logf("Handler called for OPTIONS request")
		// Print all request headers
		c.RequestCtx().Request.Header.VisitAll(func(key, value []byte) {
			t.Logf("Request Header: %s = %s", string(key), string(value))
		})
	})

	// Configure CORS with specific allowed origins
	router.Use(provider.CORS([]string{"http://allowed-origin.com"}))

	router.GET("/test", func(c r.Context) {
		c.JSON(200, map[string]string{"status": "ok"})
	})

	server := r.NewServer(r.Config{
		Handler: router,
	})

	// Test preflight request specifically
	t.Run("OPTIONS_Preflight", func(t *testing.T) {
		ctx := createTestContext()

		// Set up the preflight request
		ctx.RequestCtx().Request.Header.Set("Origin", "http://allowed-origin.com")
		ctx.RequestCtx().Request.Header.SetMethod("OPTIONS")
		ctx.RequestCtx().Request.SetRequestURI("/test")
		ctx.RequestCtx().Request.Header.Set("Access-Control-Request-Method", "GET")
		ctx.RequestCtx().Request.Header.Set("Access-Control-Request-Headers", "content-type")

		// Log the request setup
		t.Log("Request setup complete")
		t.Logf("Method: %s", string(ctx.RequestCtx().Method()))
		t.Logf("URI: %s", string(ctx.RequestCtx().URI().Path()))

		// Make the request
		server.ServeHTTP(ctx.RequestCtx())

		// Log response details
		t.Log("Response received")
		t.Logf("Status Code: %d", ctx.RequestCtx().Response.StatusCode())

		// Log all response headers
		ctx.RequestCtx().Response.Header.VisitAll(func(key, value []byte) {
			t.Logf("Response Header: %s = %s", string(key), string(value))
		})

		// Verify response
		status := ctx.RequestCtx().Response.StatusCode()
		if status != 204 {
			t.Errorf("Expected status 204, got %d", status)
		}

		cors := string(ctx.RequestCtx().Response.Header.Peek("Access-Control-Allow-Origin"))
		if cors != "http://allowed-origin.com" {
			t.Errorf("Expected CORS header http://allowed-origin.com, got %s", cors)
		}

		methods := string(ctx.RequestCtx().Response.Header.Peek("Access-Control-Allow-Methods"))
		if methods == "" {
			t.Error("Access-Control-Allow-Methods header not set for preflight request")
		}

		headers := string(ctx.RequestCtx().Response.Header.Peek("Access-Control-Allow-Headers"))
		if headers == "" {
			t.Error("Access-Control-Allow-Headers header not set for preflight request")
		}
	})
}
