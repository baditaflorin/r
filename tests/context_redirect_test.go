// File: tests/context_redirect_test.go
package r_test

import (
	"testing"
)

func TestContext_Redirect(t *testing.T) {
	ctx := createTestContext() // reuse your helper from context_test.go
	redirectURL := "http://example.com"

	// Call the Redirect method with status 302
	err := ctx.Redirect(302, redirectURL)
	if err != nil {
		t.Errorf("Redirect returned error: %v", err)
	}

	// Verify that the Location header is set correctly
	location := string(ctx.RequestCtx().Response.Header.Peek("Location"))
	if location != redirectURL {
		t.Errorf("Expected Location header %s, got %s", redirectURL, location)
	}

	// Verify that the status code is correctly set
	if ctx.RequestCtx().Response.StatusCode() != 302 {
		t.Errorf("Expected status code 302, got %d", ctx.RequestCtx().Response.StatusCode())
	}
}
