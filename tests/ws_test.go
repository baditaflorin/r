// File: tests/basic_ws_test.go
package r_test

import (
	"fmt"
	"github.com/baditaflorin/r"
	"github.com/fasthttp/websocket"
	"net"
	"sync"
	"testing"
	"time"
)

// Basic handler for testing
type testWSHandler struct {
	connectCalled   bool
	messageReceived []byte
	closeCalled     bool
	messageCount    int      // Add counter for concurrent test
	errorReceived   bool     // New field for tracking errors
	messages        [][]byte // Store all messages for verification
	mu              sync.Mutex
	t               *testing.T
}

func newTestHandler(t *testing.T) *testWSHandler {
	return &testWSHandler{
		t:        t,
		messages: make([][]byte, 0),
	}
}

func (h *testWSHandler) OnMessage(conn r.WSConnection, msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Store the message
	h.messageReceived = make([]byte, len(msg))
	copy(h.messageReceived, msg)

	// Add to messages slice
	msgCopy := make([]byte, len(msg))
	copy(msgCopy, msg)
	h.messages = append(h.messages, msgCopy)

	// Increment message count
	h.messageCount++

	h.t.Logf("OnMessage called with message length: %d", len(msg))
	if len(msg) > 0 {
		if len(msg) > 100 {
			h.t.Logf("Message too large to log, first 100 bytes: %x", msg[:100])
		} else {
			h.t.Logf("Message content: %x", msg)
		}
	}
}

func (h *testWSHandler) OnConnect(conn r.WSConnection) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connectCalled = true
	h.t.Log("OnConnect called")
}

func (h *testWSHandler) OnClose(conn r.WSConnection) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closeCalled = true
	h.t.Log("OnClose called")
}

// Helper function to start test server
func startTestServer(t *testing.T, handler *testWSHandler) (string, func()) {
	router := r.NewRouter()

	// Add debug logging
	router.Use(func(c r.Context) {
		t.Logf("Received request: %s %s", c.Method(), c.Path())
		c.Next()
	})

	router.WS("/ws", handler)

	// Create server with router
	config := r.DefaultConfig()
	config.Handler = router
	server := r.NewServer(config)

	// Find an available port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find available port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// Start server in goroutine
	serverErrCh := make(chan error, 1)
	go func() {
		if err := server.Start(fmt.Sprintf(":%d", port)); err != nil {
			serverErrCh <- err
		}
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// Check if server failed to start
	select {
	case err := <-serverErrCh:
		t.Fatalf("Server failed to start: %v", err)
	default:
		// Server started successfully
	}

	url := fmt.Sprintf("ws://127.0.0.1:%d/ws", port)
	cleanup := func() {
		if err := server.Stop(); err != nil {
			t.Logf("Error stopping server: %v", err)
		}
	}

	return url, cleanup
}

// Test 1: Basic connection establishment
func TestWebSocket_BasicConnection(t *testing.T) {
	handler := newTestHandler(t)
	url, cleanup := startTestServer(t, handler)
	defer cleanup()

	// Connect to WebSocket server
	dialer := websocket.Dialer{
		HandshakeTimeout: time.Second,
	}

	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	// Wait briefly for handler to process connection
	time.Sleep(100 * time.Millisecond)

	handler.mu.Lock()
	if !handler.connectCalled {
		t.Error("OnConnect was not called")
	}
	handler.mu.Unlock()
}

// Test 2: Single byte message
func TestWebSocket_SingleByte(t *testing.T) {
	handler := newTestHandler(t)
	url, cleanup := startTestServer(t, handler)
	defer cleanup()

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	// Send a single byte
	testByte := []byte{42}
	err = conn.WriteMessage(websocket.BinaryMessage, testByte)
	if err != nil {
		t.Fatalf("Failed to send message: %v", err)
	}

	// Wait briefly for message processing
	time.Sleep(100 * time.Millisecond)

	handler.mu.Lock()
	if handler.messageReceived == nil {
		t.Error("No message received")
	} else if len(handler.messageReceived) != 1 || handler.messageReceived[0] != 42 {
		t.Errorf("Expected byte 42, got %v", handler.messageReceived)
	}
	handler.mu.Unlock()
}

// Test 3: Connection close detection
func TestWebSocket_ConnectionClose(t *testing.T) {
	handler := newTestHandler(t)
	url, cleanup := startTestServer(t, handler)
	defer cleanup()

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	// Close connection
	conn.Close()

	// Wait briefly for close handler
	time.Sleep(100 * time.Millisecond)

	handler.mu.Lock()
	if !handler.closeCalled {
		t.Error("OnClose was not called")
	}
	handler.mu.Unlock()
}

// Test 4: Empty message handling
func TestWebSocket_EmptyMessage(t *testing.T) {
	handler := newTestHandler(t)
	url, cleanup := startTestServer(t, handler)
	defer cleanup()

	t.Log("Starting empty message test")

	// Connect to WebSocket server
	t.Log("Attempting to connect to WebSocket server")
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	if resp != nil {
		t.Logf("Connection response status: %d", resp.StatusCode)
	}
	defer conn.Close()

	// Wait for connection to establish
	time.Sleep(100 * time.Millisecond)
	t.Log("Connection established, waiting before sending message")

	// Send empty message
	t.Log("Attempting to send empty message")
	err = conn.WriteMessage(websocket.TextMessage, []byte{})
	if err != nil {
		t.Fatalf("Failed to send empty message: %v", err)
	}
	t.Log("Empty message sent successfully")

	// Wait for message processing
	t.Log("Waiting for message processing")
	time.Sleep(200 * time.Millisecond)

	handler.mu.Lock()
	defer handler.mu.Unlock()

	t.Logf("Final check - messageReceived is nil: %v", handler.messageReceived == nil)
	if handler.messageReceived == nil {
		t.Error("No message received")
	} else {
		t.Logf("Received message length: %d", len(handler.messageReceived))
	}
}

func (h *testWSHandler) OnError(conn r.WSConnection, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.errorReceived = true
	h.t.Logf("WebSocket error received: %v", err)
}
