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
			c.processSendMessage(message, ok, cfg)
		case <-ticker.C:
			if err := c.sendPing(cfg); err != nil {
				return
			}
		case <-c.closeCh:
			return
		}
	}
}

// processSendMessage processes messages received on the send channel.
func (c *wsConnection) processSendMessage(message []byte, ok bool, cfg wsConfig) {
	if !ok {
		c.sendCloseMessage(cfg)
		return
	}
	if err := c.sendOutgoingMessage(message, cfg); err != nil {
		// If sending fails, you may choose to perform additional error handling here.
		return
	}
}

// cleanupWritePump stops the ticker and rate limiter, then closes the connection.
func (c *wsConnection) cleanupWritePump(ticker *time.Ticker) {
	ticker.Stop()
	// No need to stop c.rateLimiter since rate.Limiter does not require stopping.
	c.Close()
}

// sendCloseMessage is called when the send channel is closed.
// It sends a close control message to the client.
func (c *wsConnection) sendCloseMessage(cfg wsConfig) {
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

// sendOutgoingMessage writes an outgoing message to the WebSocket client.
func (c *wsConnection) sendOutgoingMessage(message []byte, cfg wsConfig) error {
	deadline := time.Now().Add(cfg.WriteTimeout)
	c.SetWriteDeadline(deadline)
	if err := c.WriteMessage(websocket.TextMessage, message); err != nil {
		c.logger.Error("Write error", "error", err, "conn_id", c.id)
		return err
	}
	return nil
}

// sendPing sends a ping message to the client.
func (c *wsConnection) sendPing(cfg wsConfig) error {
	deadline := time.Now().Add(cfg.WriteTimeout)
	c.SetWriteDeadline(deadline)
	if err := c.WriteMessage(websocket.PingMessage, nil); err != nil {
		c.logger.Error("Ping error", "error", err, "conn_id", c.id)
		return err
	}
	return nil
}
