// File: ws_write_pump.go
package r

import (
	"github.com/fasthttp/websocket"
	"time"
)

// writePump handles sending messages and periodic pings to the WebSocket client.
func (c *wsConnection) writePump() {
	cfg := c.getWSConfig()

	ticker := time.NewTicker(cfg.PingInterval)
	defer c.cleanupWritePump(ticker)

	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				c.handleSendChannelClosed(cfg)
				return
			}
			if err := c.handleOutgoingMessage(message, cfg); err != nil {
				return
			}

		case <-ticker.C:
			if err := c.handlePing(cfg); err != nil {
				return
			}

		case <-c.closeCh:
			return
		}
	}
}

// cleanupWritePump stops the ticker and rate limiter, then closes the connection.
func (c *wsConnection) cleanupWritePump(ticker *time.Ticker) {
	ticker.Stop()
	if c.rateLimiter != nil {
		c.rateLimiter.Stop()
	}
	c.Close()
}

// handleSendChannelClosed is called when the send channel is closed.
// It sends a close control message to the client.
func (c *wsConnection) handleSendChannelClosed(cfg wsConfig) {
	deadline := time.Now().Add(cfg.WriteTimeout)
	err := c.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		deadline,
	)
	if err != nil {
		c.logger.Error("Error sending close message", "error", err, "conn_id", c.id)
	}
}

// handleOutgoingMessage writes an outgoing message to the WebSocket client.
func (c *wsConnection) handleOutgoingMessage(message []byte, cfg wsConfig) error {
	deadline := time.Now().Add(cfg.WriteTimeout)
	c.SetWriteDeadline(deadline)
	if err := c.WriteMessage(websocket.TextMessage, message); err != nil {
		c.logger.Error("Write error", "error", err, "conn_id", c.id)
		return err
	}
	return nil
}

// handlePing sends a ping message to the client.
func (c *wsConnection) handlePing(cfg wsConfig) error {
	deadline := time.Now().Add(cfg.WriteTimeout)
	c.SetWriteDeadline(deadline)
	if err := c.WriteMessage(websocket.PingMessage, nil); err != nil {
		c.logger.Error("Ping error", "error", err, "conn_id", c.id)
		return err
	}
	return nil
}
