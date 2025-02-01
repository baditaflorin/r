// File: tests/context_stream_test.go
package r_test

import (
	"bytes"
	"testing"
)

// TestContext_Stream verifies that streaming a response works as expected.
func TestContext_Stream(t *testing.T) {
	ctx := createTestContext()
	expectedContent := "This is streamed content."
	reader := bytes.NewReader([]byte(expectedContent))

	// Call Stream with a 200 status code and content type.
	err := ctx.Stream(200, "text/plain", reader)
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}

	// Verify that the response body contains the streamed content.
	body := string(ctx.RequestCtx().Response.Body())
	if body != expectedContent {
		t.Errorf("Expected streamed content '%s', got '%s'", expectedContent, body)
	}

	// Verify that the Content-Type header is set correctly.
	contentType := string(ctx.RequestCtx().Response.Header.Peek("Content-Type"))
	if contentType != "text/plain" {
		t.Errorf("Expected Content-Type 'text/plain', got '%s'", contentType)
	}

	// Verify that the status code is set to 200.
	if ctx.RequestCtx().Response.StatusCode() != 200 {
		t.Errorf("Expected status code 200, got %d", ctx.RequestCtx().Response.StatusCode())
	}
}
