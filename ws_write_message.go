// File: ws_write_message.go
package r

import (
	"fmt"
	"time"
)

// WriteMessage writes a message to the WebSocket connection in a rate-limited way.
func (c *wsConnection) WriteMessage(messageType int, data []byte) error {
	// Perform basic connection validation.
	if err := c.validateConnection(); err != nil {
		return err
	}

	// Ensure exclusive access.
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if the connection is properly initialized.
	if err := c.checkInitialization(); err != nil {
		return err
	}

	// Attempt to send the message using the rate limiter.
	return c.sendMessageWithRateLimit(data)
}

// validateConnection performs nil and state checks.
func (c *wsConnection) validateConnection() error {
	if c == nil || c.Conn == nil {
		return fmt.Errorf("connection is nil")
	}
	if c.state.Load() != wsStateActive {
		return ErrConnectionClosed
	}
	return nil
}

// checkInitialization verifies that the connection's write buffer and rate limiter are set.
func (c *wsConnection) checkInitialization() error {
	if c.writeBuffer == nil || c.rateLimiter == nil {
		return fmt.Errorf("connection not properly initialized")
	}
	return nil
}

// sendMessageWithRateLimit tries to send the message by first waiting on the rate limiter.
func (c *wsConnection) sendMessageWithRateLimit(data []byte) error {
	// Wait for the rate limiter to allow sending.
	select {
	case <-c.rateLimiter.C:
		return c.enqueueMessage(data)
	default:
		c.incrementDroppedCounter("rate_limited")
		return ErrRateLimited
	}
}

// enqueueMessage attempts to queue the message into the write buffer with a short timeout.
func (c *wsConnection) enqueueMessage(data []byte) error {
	select {
	case c.writeBuffer <- data:
		c.incrementBufferedCounter()
		return nil
	case <-time.After(10 * time.Millisecond):
		if c.dropMessagesOnFull {
			c.incrementDroppedCounter("buffer_full")
			return ErrBufferFull
		}
		// If messages are not dropped on full, you might choose to block or handle differently.
		return ErrBufferFull
	}
}

// incrementBufferedCounter increases the counter for successfully buffered messages.
func (c *wsConnection) incrementBufferedCounter() {
	if c.metrics != nil {
		c.metrics.IncrementCounter("ws.messages.buffered", map[string]string{
			"conn_id": c.id,
		})
	}
}

// incrementDroppedCounter increases the counter for dropped messages and logs the reason.
func (c *wsConnection) incrementDroppedCounter(reason string) {
	if c.metrics != nil {
		c.metrics.IncrementCounter("ws.messages.dropped", map[string]string{
			"reason":  reason,
			"conn_id": c.id,
		})
	}
}
