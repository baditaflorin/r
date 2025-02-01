// File: tests/context_stream_test.go
package r_test

import (
	"bytes"
	"fmt"
	"testing"
)

// TestContext_Stream verifies that streaming a response works as expected.
func TestContext_Stream(t *testing.T) {
	ctx := CreateTestContext()
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

// errorReader simulates a reader that fails after a first successful read.
type errorReader struct {
	readCount int
}

func (e *errorReader) Read(p []byte) (int, error) {
	if e.readCount > 0 {
		return 0, fmt.Errorf("simulated read error")
	}
	e.readCount++
	return copy(p, []byte("partial content")), nil
}

func TestContext_Stream_Error(t *testing.T) {
	ctx := CreateTestContext()
	er := &errorReader{}
	err := ctx.Stream(200, "text/plain", er)
	if err == nil {
		t.Error("Expected an error from Stream due to a failing reader, but got nil")
	} else {
		t.Logf("Stream returned expected error: %v", err)
	}
}
