// File: ws_read_pump.go
package r

import (
	"bytes"
	"fmt"
	"github.com/fasthttp/websocket"
	"io"
	"runtime/debug"
	"time"
)

// readPump handles incoming messages from the WebSocket connection.
func (c *wsConnection) readPump() {
	// Recover from panics and ensure the connection is closed.
	defer c.recoverAndClose()

	// Retrieve dedicated WebSocket configuration.
	cfg := c.getWSConfig()

	// Configure read-related settings and handlers.
	c.setupReadHandlers(cfg)

	// Main loop: continuously read messages.
	for {
		messageType, reader, err := c.NextReader()
		if err != nil {
			c.handleNextReaderError(err)
			return
		}

		if err := c.processReader(reader, messageType, cfg.ReadLimit); err != nil {
			return
		}
	}
}

// recoverAndClose recovers from any panic in readPump and closes the connection.
func (c *wsConnection) recoverAndClose() {
	if r := recover(); r != nil {
		c.logger.Error("Panic in WebSocket read pump",
			"error", fmt.Sprintf("%v", r),
			"stack", string(debug.Stack()),
			"conn_id", c.id)
	}
	c.Close()
}

// setupReadHandlers configures read limits, deadlines, and handlers for close and pong events.
func (c *wsConnection) setupReadHandlers(cfg wsConfig) {
	c.SetReadLimit(cfg.ReadLimit)
	c.SetReadDeadline(time.Now().Add(cfg.PongWait))

	// Set the close handler to capture close code and reason.
	c.SetCloseHandler(func(code int, text string) error {
		c.mu.Lock()
		c.closeCode = code
		c.closeReason = text
		c.mu.Unlock()
		c.state.Store(wsStateClosed)
		return nil
	})

	// Refresh the read deadline whenever a pong is received.
	c.SetPongHandler(func(string) error {
		c.SetReadDeadline(time.Now().Add(cfg.PongWait))
		return nil
	})
}

// handleNextReaderError processes errors returned by NextReader.
func (c *wsConnection) handleNextReaderError(err error) {
	if websocket.IsUnexpectedCloseError(err,
		websocket.CloseGoingAway,
		websocket.CloseAbnormalClosure) {
		c.logger.Error("WebSocket read error",
			"error", err,
			"conn_id", c.id)
	}
}

// processReader reads a complete message from the reader, enforcing the provided limit.
// If a valid text or binary message is received, it is forwarded to the connection's handler.
func (c *wsConnection) processReader(reader io.Reader, messageType int, limit int64) error {
	msg, err := readMessageWithLimit(reader, limit)
	if err != nil {
		c.logger.Error("Failed to read message", "error", err, "conn_id", c.id)
		c.handleMessageError("Failed to read message")
		return err
	}

	if messageType == websocket.TextMessage || messageType == websocket.BinaryMessage {
		// Copy the message so the handler receives its own buffer.
		msgCopy := make([]byte, len(msg))
		copy(msgCopy, msg)
		if c.handler != nil {
			c.handler.OnMessage(c, msgCopy)
		}
		c.msgCount.Add(1)
	}

	return nil
}

// readMessageWithLimit reads all data from r using an io.LimitReader.
// It returns an error if the message exceeds the provided limit.
func readMessageWithLimit(r io.Reader, limit int64) ([]byte, error) {
	limitedReader := io.LimitReader(r, limit+1) // +1 byte to detect overflow
	buf := new(bytes.Buffer)
	n, err := io.Copy(buf, limitedReader)
	if err != nil {
		return nil, err
	}
	if n > limit {
		return nil, fmt.Errorf("message size exceeds limit of %d bytes", limit)
	}
	return buf.Bytes(), nil
}
