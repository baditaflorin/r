package r_test

import (
	"bytes"
	"encoding/json"
	"github.com/baditaflorin/r"
	"testing"
)

func TestLogger_Info(t *testing.T) {
	logger := r.NewStructuredLogger(nil, r.InfoLevel)
	logger.Info("Test log")
}

func TestLogger_DebugLevel(t *testing.T) {
	logger := r.NewStructuredLogger(nil, r.DebugLevel)
	logger.Debug("Debugging info")
}

func TestLogger_WithFields(t *testing.T) {
	var buf bytes.Buffer
	logger := r.NewStructuredLogger(&buf, r.InfoLevel)

	fields := map[string]interface{}{
		"user_id": "123",
		"action":  "login",
	}

	loggerWithFields := logger.WithFields(fields)
	loggerWithFields.Info("User logged in")

	var logEntry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("Failed to parse log entry: %v", err)
	}

	fieldsMap, ok := logEntry["fields"].(map[string]interface{})
	if !ok {
		t.Fatal("Fields not found in log entry")
	}

	if fieldsMap["user_id"] != "123" || fieldsMap["action"] != "login" {
		t.Error("Fields not properly included in log entry")
	}
}

func TestLogger_ErrorWithStack(t *testing.T) {
	var buf bytes.Buffer
	logger := r.NewStructuredLogger(&buf, r.ErrorLevel)

	logger.Error("Test error message")

	var logEntry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("Failed to parse log entry: %v", err)
	}

	if logEntry["stack_trace"] == nil {
		t.Error("Stack trace not included in error log")
	}
}
