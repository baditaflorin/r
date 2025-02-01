// File: tests/context_error_response_test.go
package r_test

import (
	"testing"
)

func TestContext_ErrorResponse(t *testing.T) {
	ctx := CreateTestContext()

	// Call ErrorResponse to send an error message and abort further processing.
	ctx.ErrorResponse(400, "bad request")

	// Verify that the status code is set correctly.
	if ctx.RequestCtx().Response.StatusCode() != 400 {
		t.Errorf("Expected status code 400, got %d", ctx.RequestCtx().Response.StatusCode())
	}

	// Verify that the response body contains the error message.
	body := string(ctx.RequestCtx().Response.Body())
	if body != "bad request" {
		t.Errorf("Expected response body 'bad request', got '%s'", body)
	}
}
