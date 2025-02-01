// File: tests/ws_advanced_test.go
package r_test

import (
	"bytes"
	"fmt"
	"github.com/fasthttp/websocket"
	"sync"
	"testing"
	"time"
)

// Test 1: Multiple Sequential Messages
func TestWebSocket_MultipleMessages(t *testing.T) {
	handler := newTestHandler(t)
	url, cleanup := startTestServer(t, handler)
	defer cleanup()

	// Connect to WebSocket server
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	// Test messages
	messages := [][]byte{
		[]byte("Hello"),
		[]byte("World"),
		[]byte("!"),
	}

	// Send messages with delay between each
	for i, msg := range messages {
		t.Logf("Sending message %d: %s", i+1, string(msg))
		err := conn.WriteMessage(websocket.TextMessage, msg)
		if err != nil {
			t.Fatalf("Failed to send message %d: %v", i+1, err)
		}

		// Wait for message processing
		time.Sleep(50 * time.Millisecond)

		handler.mu.Lock()
		if !bytes.Equal(handler.messageReceived, msg) {
			t.Errorf("Message %d mismatch: expected %s, got %s",
				i+1, string(msg), string(handler.messageReceived))
		}
		handler.mu.Unlock()
	}
}

// Test 2: Large Message Handling
func TestWebSocket_LargeMessage(t *testing.T) {
	handler := newTestHandler(t)
	url, cleanup := startTestServer(t, handler)
	defer cleanup()

	// Connect with custom dialer for large messages
	dialer := websocket.Dialer{
		ReadBufferSize:  1024 * 1024, // 1MB read buffer
		WriteBufferSize: 1024 * 1024, // 1MB write buffer
	}

	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	// Create a large message (100KB)
	size := 100 * 1024
	largeMsg := make([]byte, size)
	for i := 0; i < size; i++ {
		largeMsg[i] = byte(i % 256)
	}

	t.Logf("Sending large message of size: %d bytes", len(largeMsg))

	// Write message with a deadline
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err = conn.WriteMessage(websocket.BinaryMessage, largeMsg)
	if err != nil {
		t.Fatalf("Failed to send large message: %v", err)
	}

	// Wait longer for large message processing
	time.Sleep(500 * time.Millisecond)

	handler.mu.Lock()
	receivedSize := len(handler.messageReceived)
	handler.mu.Unlock()

	if receivedSize != size {
		t.Errorf("Large message size mismatch: expected %d bytes, got %d bytes",
			size, receivedSize)
		return // Return here to prevent panic
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()

	// Verify content of first and last few bytes
	for i := 0; i < 10; i++ {
		if handler.messageReceived[i] != largeMsg[i] {
			t.Errorf("Content mismatch at beginning, byte %d: expected %d, got %d",
				i, largeMsg[i], handler.messageReceived[i])
		}
		if handler.messageReceived[size-i-1] != largeMsg[size-i-1] {
			t.Errorf("Content mismatch at end, byte %d: expected %d, got %d",
				size-i-1, largeMsg[size-i-1], handler.messageReceived[size-i-1])
		}
	}
}

// Test 3: Concurrent Clients
func TestWebSocket_ConcurrentClients(t *testing.T) {
	handler := newTestHandler(t)
	url, cleanup := startTestServer(t, handler)
	defer cleanup()

	const numClients = 5
	const messagesPerClient = 10

	var wg sync.WaitGroup
	clientErrors := make(chan error, numClients*messagesPerClient)

	// Start multiple clients
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			// Connect client
			conn, _, err := websocket.DefaultDialer.Dial(url, nil)
			if err != nil {
				clientErrors <- fmt.Errorf("client %d failed to connect: %v", clientID, err)
				return
			}
			defer conn.Close()

			// Send messages
			for j := 0; j < messagesPerClient; j++ {
				msg := []byte(fmt.Sprintf("Client %d Message %d", clientID, j))
				err := conn.WriteMessage(websocket.TextMessage, msg)
				if err != nil {
					clientErrors <- fmt.Errorf("client %d failed to send message %d: %v",
						clientID, j, err)
					return
				}

				// Small delay between messages
				time.Sleep(10 * time.Millisecond)
			}
		}(i)
	}

	// Wait for all clients to finish
	wg.Wait()
	close(clientErrors)

	// Check for any errors
	for err := range clientErrors {
		t.Error(err)
	}

	// Verify message count
	time.Sleep(100 * time.Millisecond) // Wait for final messages to be processed

	handler.mu.Lock()
	messageCount := handler.messageCount // Add a messageCount field to testWSHandler
	handler.mu.Unlock()

	expectedMessages := numClients * messagesPerClient
	if messageCount != expectedMessages {
		t.Errorf("Message count mismatch: expected %d, got %d",
			expectedMessages, messageCount)
	}
}

func TestWebSocket_OversizedMessage(t *testing.T) {
	handler := newTestHandler(t)
	url, cleanup := startTestServer(t, handler)
	defer cleanup()

	// Connect with custom dialer for large messages
	dialer := websocket.Dialer{
		ReadBufferSize:   2 * 1024 * 1024, // 2MB read buffer
		WriteBufferSize:  2 * 1024 * 1024, // 2MB write buffer
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	// Create an oversized message (20MB)
	size := 20 * 1024 * 1024 // 20MB
	oversizedMsg := make([]byte, size)

	// Fill with recognizable pattern
	for i := 0; i < size; i++ {
		oversizedMsg[i] = byte(i % 256)
	}

	t.Logf("Attempting to send oversized message of size: %d bytes", len(oversizedMsg))

	// Create a channel to track connection closure
	closeError := make(chan error, 1)
	go func() {
		_, _, err := conn.ReadMessage() // Wait for server response or close
		closeError <- err
	}()

	// Write message with a deadline
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err = conn.WriteMessage(websocket.BinaryMessage, oversizedMsg)

	// Wait for either connection close or timeout
	select {
	case err := <-closeError:
		if err == nil {
			t.Error("Expected connection to be closed with error")
		} else {
			t.Logf("Connection closed as expected with error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Timeout waiting for connection close")
	}

	// Verify handler state
	handler.mu.Lock()
	defer handler.mu.Unlock()

	// Check if we received an error message in the handler
	if !handler.errorReceived {
		t.Error("Expected handler to receive error notification")
	}

	// Verify the connection was properly closed
	if !handler.closeCalled {
		t.Error("Expected handler OnClose to be called")
	}

	// Log final statistics
	t.Logf("Final handler state - Messages received: %d, Errors received: %v, Connection closed: %v",
		handler.messageCount,
		handler.errorReceived,
		handler.closeCalled)
}
