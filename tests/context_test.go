package r_test

import (
	"encoding/json"
	"fmt"
	"github.com/baditaflorin/r"
	"github.com/qiangxue/fasthttp-routing"
	"github.com/valyala/fasthttp"
	"testing"
	"time"
)

// createTestContext creates a new context for testing
func createTestContext() r.Context {
	ctx := &fasthttp.RequestCtx{}
	routingCtx := &routing.Context{RequestCtx: ctx}
	return r.NewTestContext(routingCtx)
}

func TestContext_SetAndGet(t *testing.T) {
	ctx := createTestContext()
	ctx.Set("key", "value")
	if val, ok := ctx.Get("key"); !ok || val != "value" {
		t.Errorf("Expected context value 'value', got %v", val)
	}
}

func TestContext_Timeout(t *testing.T) {
	ctx := createTestContext()
	timeoutCtx, cancel := r.TestContextWithTimeout(ctx, 1*time.Second)
	defer cancel()

	time.Sleep(2 * time.Second)
	if timeoutCtx.Err() == nil {
		t.Errorf("Expected context to timeout")
	}
}

func TestContext_RequestID(t *testing.T) {
	ctx := createTestContext()
	if ctx.RequestID() == "" {
		t.Errorf("Expected request ID to be generated")
	}
}

func createTestContextWithMethod(method string) r.Context {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(method)
	routingCtx := &routing.Context{RequestCtx: ctx}
	return r.NewTestContext(routingCtx)
}

func TestContext_Method(t *testing.T) {
	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}
	for _, method := range methods {
		ctx := createTestContextWithMethod(method)
		if ctx.Method() != method {
			t.Errorf("Expected method %s, got %s", method, ctx.Method())
		}
	}
}

func TestContext_Path(t *testing.T) {
	ctx := createTestContext()
	ctx.RequestCtx().Request.SetRequestURI("/test/path")
	if ctx.Path() != "/test/path" {
		t.Errorf("Expected path /test/path, got %s", ctx.Path())
	}
}

func TestContext_QueryParam(t *testing.T) {
	ctx := createTestContext()
	ctx.RequestCtx().Request.SetRequestURI("/test?name=value")
	if param := ctx.QueryParam("name"); param != "value" {
		t.Errorf("Expected query parameter 'value', got %s", param)
	}
}

func TestContext_Headers(t *testing.T) {
	ctx := createTestContext()
	ctx.SetHeader("X-Test", "test-value")
	if value := ctx.GetHeader("X-Test"); value != "test-value" {
		t.Errorf("Expected header value 'test-value', got %s", value)
	}
}

func TestContext_JSON(t *testing.T) {
	ctx := createTestContext()
	testData := map[string]string{"key": "value"}

	if err := ctx.JSON(200, testData); err != nil {
		t.Errorf("Failed to write JSON response: %v", err)
	}

	contentType := ctx.GetHeader("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected content type application/json, got %s", contentType)
	}

	var result map[string]string
	if err := json.Unmarshal(ctx.RequestCtx().Response.Body(), &result); err != nil {
		t.Errorf("Failed to parse JSON response: %v", err)
	}

	if result["key"] != "value" {
		t.Errorf("Expected JSON value 'value', got %s", result["key"])
	}
}

func TestContext_String(t *testing.T) {
	ctx := createTestContext()
	testString := "test response"

	if err := ctx.String(200, testString); err != nil {
		t.Errorf("Failed to write string response: %v", err)
	}

	response := string(ctx.RequestCtx().Response.Body())
	if response != testString {
		t.Errorf("Expected string response '%s', got '%s'", testString, response)
	}
}

func TestContext_RealIP(t *testing.T) {
	ctx := createTestContext()
	testIP := "127.0.0.1"

	ctx.SetHeader("X-Real-IP", testIP)
	if ip := ctx.RealIP(); ip != testIP {
		t.Errorf("Expected IP %s from X-Real-IP, got %s", testIP, ip)
	}

	ctx.SetHeader("X-Real-IP", "")
	ctx.SetHeader("X-Forwarded-For", testIP)
	if ip := ctx.RealIP(); ip != testIP {
		t.Errorf("Expected IP %s from X-Forwarded-For, got %s", testIP, ip)
	}
}

// Fixed test implementation
func TestContext_IsWebSocket(t *testing.T) {
	ctx := createTestContext()

	// First check when it's not a WebSocket request
	if ctx.IsWebSocket() {
		t.Error("Expected non-WebSocket request")
	}

	// Set the Upgrade header directly on the underlying RequestCtx
	ctx.RequestCtx().Request.Header.Set("Upgrade", "websocket")

	if !ctx.IsWebSocket() {
		t.Error("Expected WebSocket request")
	}
}

func TestContext_Abort(t *testing.T) {
	routingCtx := &routing.Context{RequestCtx: &fasthttp.RequestCtx{}}
	handlerCalled := false

	handlers := []r.HandlerFunc{
		func(c r.Context) {
			c.Abort()
		},
		func(c r.Context) {
			handlerCalled = true
		},
	}

	ctx := r.TestContextWithHandlers(routingCtx, handlers)
	ctx.Next()

	if handlerCalled {
		t.Error("Expected abort to prevent subsequent handlers from being called")
	}
}

func TestContext_AbortWithError(t *testing.T) {
	routingCtx := &routing.Context{RequestCtx: &fasthttp.RequestCtx{}}
	handlerCalled := false

	handlers := []r.HandlerFunc{
		func(c r.Context) {
			c.AbortWithError(500, fmt.Errorf("test error"))
		},
		func(c r.Context) {
			handlerCalled = true
		},
	}

	ctx := r.TestContextWithHandlers(routingCtx, handlers)
	ctx.Next()

	if handlerCalled {
		t.Error("Expected abort to prevent subsequent handlers from being called")
	}

	if err := ctx.Error(); err == nil || err.Error() != "test error" {
		t.Errorf("Expected error 'test error', got %v", err)
	}

	if ctx.RequestCtx().Response.StatusCode() != 500 {
		t.Errorf("Expected status code 500, got %d", ctx.RequestCtx().Response.StatusCode())
	}
}
