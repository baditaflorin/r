// File: tests/connection_manager_stats_test.go
package r_test

import (
	"testing"
	"time"

	"github.com/baditaflorin/r"
	"github.com/fasthttp/websocket"
)

func TestConnectionManager_GetStats(t *testing.T) {
	// Start a WebSocket server.
	handler := newTestHandler(t)
	url, cleanup := startTestServer(t, handler)
	defer cleanup()

	// Connect a WebSocket client.
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	// Allow some time for the connection to register.
	time.Sleep(100 * time.Millisecond)
	stats := r.GetConnectionStats()
	if active, ok := stats["active_connections"].(int32); !ok || active < 1 {
		t.Errorf("Expected active connections >= 1, got %v", stats["active_connections"])
	}

	// Close the connection.
	conn.Close()
	// Wait a short time for cleanup to occur.
	time.Sleep(100 * time.Millisecond)
	stats = r.GetConnectionStats()
	if active, ok := stats["active_connections"].(int32); !ok || active != 0 {
		t.Errorf("Expected active connections to be 0 after close, got %v", stats["active_connections"])
	}
}
