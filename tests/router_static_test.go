// File: tests/router_static_test.go
package r_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/baditaflorin/r"
)

func TestRouter_StaticFileServing(t *testing.T) {
	// Create a temporary directory and a test file within it.
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")
	expectedContent := "Hello Static"
	if err := os.WriteFile(filePath, []byte(expectedContent), fs.ModePerm); err != nil {
		t.Fatalf("Failed to write temporary file: %v", err)
	}

	// Create a new router and add a static file route.
	router := r.NewRouter()
	router.Static("/static", tmpDir)

	// Create a test context simulating a GET request for the file.
	ctx := CreateTestContext()
	ctx.RequestCtx().Request.Header.SetMethod("GET")
	ctx.RequestCtx().Request.SetRequestURI("/static/test.txt")

	// Process the request.
	router.ServeHTTP(ctx.RequestCtx())

	// Verify the file content returned in the response.
	body := string(ctx.RequestCtx().Response.Body())
	if body != expectedContent {
		t.Errorf("Expected file content '%s', got '%s'", expectedContent, body)
	}
}
