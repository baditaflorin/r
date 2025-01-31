package r_test

import (
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
