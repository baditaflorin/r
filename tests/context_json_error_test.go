// File: tests/context_json_error_test.go
package r_test

import (
	"testing"
)

// TestContext_JSONError verifies that passing an unencodable value results in an error.
func TestContext_JSONError(t *testing.T) {
	ctx := createTestContext()

	// Use a channel, which is not JSON serializable.
	invalidValue := make(chan int)

	err := ctx.JSON(200, invalidValue)
	if err == nil {
		t.Error("Expected JSON encoding to fail, but no error was returned")
	}
}
