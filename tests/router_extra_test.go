// File: tests/router_extra_test.go
package r_test

import (
	routing "github.com/qiangxue/fasthttp-routing"
	"github.com/valyala/fasthttp"
	"net/http"
	"testing"

	"github.com/baditaflorin/r"
)

// createTestContext properly initializes a fasthttp.RequestCtx.
func CreateTestContextSame() r.Context {
	req := fasthttp.AcquireRequest()
	req.SetRequestURI("/user/12345")
	req.Header.SetHost("localhost") // set the Host header
	req.Header.SetMethod("GET")     // set the method early on
	ctx := new(fasthttp.RequestCtx)
	ctx.Init(req, nil, nil)
	routingCtx := &routing.Context{RequestCtx: ctx}
	return r.NewTestContext(routingCtx)
}

// Test 2: Static versus dynamic route conflict.
// Register both a static route and a parameterized route whose patterns overlap.
// The static route should be used when the path exactly matches.
func TestStaticVsDynamicRouteConflict(t *testing.T) {
	router := r.NewRouter()

	// Static route for /api/status.
	router.GET("/api/status", func(c r.Context) {
		c.String(200, "static status")
	})
	// Dynamic route that would match any /api/:name.
	router.GET("/api/:name", func(c r.Context) {
		name := c.Param("name")
		c.String(200, "dynamic: "+name)
	})

	// Request /api/status – expect the static handler.
	ctx := CreateTestContextSame()
	ctx.RequestCtx().Request.Header.SetMethod("GET")
	ctx.RequestCtx().Request.SetRequestURI("/api/status")
	router.ServeHTTP(ctx.RequestCtx())

	body := string(ctx.RequestCtx().Response.Body())
	if body != "static status" {
		t.Errorf("Expected 'static status', got '%s'", body)
	}
}

// Test 3: Method-based routing.
// Register the same URI with different HTTP methods and verify that the correct
// handler is invoked for each method.
func TestMethodBasedRouting(t *testing.T) {
	router := r.NewRouter()

	router.GET("/resource", func(c r.Context) {
		c.String(200, "GET resource")
	})
	router.POST("/resource", func(c r.Context) {
		c.String(200, "POST resource")
	})

	// Test GET
	ctxGet := CreateTestContextSame()
	ctxGet.RequestCtx().Request.Header.SetMethod("GET")
	ctxGet.RequestCtx().Request.SetRequestURI("/resource")
	router.ServeHTTP(ctxGet.RequestCtx())
	if string(ctxGet.RequestCtx().Response.Body()) != "GET resource" {
		t.Errorf("GET /resource expected 'GET resource', got '%s'", string(ctxGet.RequestCtx().Response.Body()))
	}

	// Test POST
	ctxPost := CreateTestContextSame()
	ctxPost.RequestCtx().Request.Header.SetMethod("POST")
	ctxPost.RequestCtx().Request.SetRequestURI("/resource")
	router.ServeHTTP(ctxPost.RequestCtx())
	if string(ctxPost.RequestCtx().Response.Body()) != "POST resource" {
		t.Errorf("POST /resource expected 'POST resource', got '%s'", string(ctxPost.RequestCtx().Response.Body()))
	}

	// Test PUT (which has not been registered) should result in method not allowed (or not found).
	ctxPut := CreateTestContextSame()
	ctxPut.RequestCtx().Request.Header.SetMethod("PUT")
	ctxPut.RequestCtx().Request.SetRequestURI("/resource")
	router.ServeHTTP(ctxPut.RequestCtx())
	if ctxPut.RequestCtx().Response.StatusCode() != http.StatusMethodNotAllowed &&
		ctxPut.RequestCtx().Response.StatusCode() != http.StatusNotFound {
		t.Errorf("PUT /resource expected 405 or 404, got %d", ctxPut.RequestCtx().Response.StatusCode())
	}
}

// Test 4: Route registration and metadata.
// This test verifies that when routes are registered they appear in the router's registry.
func TestRouteRegistrationAndMetadata(t *testing.T) {
	router := r.NewRouter()

	router.GET("/test1", func(c r.Context) {
		c.String(200, "test1")
	})
	router.POST("/test2", func(c r.Context) {
		c.String(200, "test2")
	})

	// Retrieve registered routes from the router.
	routes := router.(*r.RouterImpl).GetRoutes()
	foundTest1 := false
	foundTest2 := false

	for _, route := range routes {
		if route.Method == "GET" && route.Path == "/test1" {
			foundTest1 = true
		}
		if route.Method == "POST" && route.Path == "/test2" {
			foundTest2 = true
		}
	}

	if !foundTest1 {
		t.Error("Route GET /test1 not found in registered routes")
	}
	if !foundTest2 {
		t.Error("Route POST /test2 not found in registered routes")
	}
}
