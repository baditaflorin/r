package r_test

import (
	"encoding/json"
	"github.com/baditaflorin/r"
	"net/http"
	"testing"
	"time"
)

// TestRouter_BasicRouting tests the basic routing functionality
func TestRouter_BasicRouting(t *testing.T) {
	router := r.NewRouter()

	// Test data
	type testResponse struct {
		Message string `json:"message"`
	}

	// Add routes
	router.GET("/test", func(c r.Context) {
		c.JSON(http.StatusOK, testResponse{Message: "GET"})
	})

	router.POST("/test", func(c r.Context) {
		c.JSON(http.StatusOK, testResponse{Message: "POST"})
	})

	// Create test server
	server := r.NewServer(r.Config{
		Handler:      router,
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 1 * time.Second,
	})

	// Test GET request
	ctx := CreateTestContext()
	ctx.RequestCtx().Request.Header.SetMethod("GET")
	ctx.RequestCtx().Request.SetRequestURI("/test")

	server.ServeHTTP(ctx.RequestCtx())

	var response testResponse
	err := json.Unmarshal(ctx.RequestCtx().Response.Body(), &response)
	if err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.Message != "GET" {
		t.Errorf("Expected GET response, got %s", response.Message)
	}

	// Test POST request
	ctx = CreateTestContext()
	ctx.RequestCtx().Request.Header.SetMethod("POST")
	ctx.RequestCtx().Request.SetRequestURI("/test")

	server.ServeHTTP(ctx.RequestCtx())

	err = json.Unmarshal(ctx.RequestCtx().Response.Body(), &response)
	if err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.Message != "POST" {
		t.Errorf("Expected POST response, got %s", response.Message)
	}
}

// TestRouter_GroupRouting tests route grouping functionality
func TestRouter_GroupRouting(t *testing.T) {
	router := r.NewRouter()

	// Create API group
	api := router.Group("/api")
	api.GET("/test", func(c r.Context) {
		c.JSON(http.StatusOK, map[string]string{"path": c.Path()})
	})

	// Test request
	ctx := CreateTestContext()
	ctx.RequestCtx().Request.Header.SetMethod("GET")
	ctx.RequestCtx().Request.SetRequestURI("/api/test")

	// Use router directly
	router.ServeHTTP(ctx.RequestCtx())

	var response map[string]string
	err := json.Unmarshal(ctx.RequestCtx().Response.Body(), &response)
	if err != nil {
		t.Fatalf("Failed to parse response: %v\nResponse body: %s", err, ctx.RequestCtx().Response.Body())
	}

	if response["path"] != "/api/test" {
		t.Errorf("Expected path /api/test, got %s", response["path"])
	}
}

// TestRouter_Middleware tests middleware functionality
func TestRouter_Middleware(t *testing.T) {
	router := r.NewRouter()

	// Add test middleware
	router.Use(func(c r.Context) {
		c.Set("test_key", "middleware_value")
		c.Next()
	})

	router.GET("/middleware-test", func(c r.Context) {
		val, exists := c.Get("test_key")
		if !exists {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": "middleware value not found"})
			return
		}
		c.JSON(http.StatusOK, map[string]string{"value": val.(string)})
	})

	// Create test server with zero timeout for synchronous handling
	server := r.NewServer(r.Config{
		Handler: router,
	})

	// Test request
	ctx := CreateTestContext()
	ctx.RequestCtx().Request.Header.SetMethod("GET")
	ctx.RequestCtx().Request.SetRequestURI("/middleware-test")

	server.BuildHandler()(ctx.RequestCtx())

	if ctx.RequestCtx().Response.StatusCode() != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, ctx.RequestCtx().Response.StatusCode())
		t.Logf("Response body: %s", ctx.RequestCtx().Response.Body())
		return
	}

	var response map[string]string
	err := json.Unmarshal(ctx.RequestCtx().Response.Body(), &response)
	if err != nil {
		t.Fatalf("Failed to parse response: %v\nResponse body: %s", err, ctx.RequestCtx().Response.Body())
	}

	if response["value"] != "middleware_value" {
		t.Errorf("Expected middleware_value, got %s", response["value"])
	}
}

//// TestRouter_ErrorHandling tests error handling functionality
//func TestRouter_ErrorHandling(t *testing.T) {
//	router := r.NewRouter()
//
//	router.GET("/error", func(c r.Context) {
//		c.AbortWithError(http.StatusBadRequest, fmt.Errorf("test error"))
//	})
//
//	// Create test server with zero timeout for synchronous handling
//	server := r.NewServer(r.Config{
//		Handler: router,
//	})
//
//	// Test request
//	ctx := CreateTestContext()
//	ctx.RequestCtx().Request.Header.SetMethod("GET")
//	ctx.RequestCtx().Request.SetRequestURI("/error")
//
//	server.BuildHandler()(ctx.RequestCtx())
//
//	if ctx.RequestCtx().Response.StatusCode() != http.StatusBadRequest {
//		t.Errorf("Expected status code %d, got %d", http.StatusBadRequest, ctx.RequestCtx().Response.StatusCode())
//	}
//}
