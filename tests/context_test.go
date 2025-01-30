package r_test

import (
	"encoding/json"
	"github.com/baditaflorin/r"
	"github.com/qiangxue/fasthttp-routing"
	"github.com/valyala/fasthttp"
	"testing"
	"time"
)

// createTestContext creates a new context for testing
func createTestContext() *r.ContextImpl {
	// Create a new fasthttp request context
	ctx := &fasthttp.RequestCtx{}
	// Create a new routing context
	routingCtx := &routing.Context{RequestCtx: ctx}
	// Create our context implementation
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
	timeoutCtx, cancel := ctx.WithTimeout(1 * time.Second)
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

func createTestContextWithMethod(method string) *r.ContextImpl {
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

	// Set the header using the underlying fasthttp RequestCtx
	ctx.RequestCtx().Response.Header.Set("X-Test", "test-value")

	// Verify the header was set correctly
	value := string(ctx.RequestCtx().Response.Header.Peek("X-Test"))
	if value != "test-value" {
		t.Errorf("Expected header value 'test-value', got %s", value)
	}

	// Test the higher-level Context interface methods
	ctx.SetHeader("X-Test-2", "test-value-2")
	if value2 := ctx.GetHeader("X-Test-2"); value2 != "test-value-2" {
		t.Errorf("Expected header value 'test-value-2', got %s", value2)
	}
}

func TestContext_JSON(t *testing.T) {
	ctx := createTestContext()
	testData := map[string]string{"key": "value"}

	if err := ctx.JSON(200, testData); err != nil {
		t.Errorf("Failed to write JSON response: %v", err)
	}

	contentType := string(ctx.RequestCtx().Response.Header.ContentType())
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
