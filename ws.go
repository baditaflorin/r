package r

import (
	"fmt"
	"github.com/fasthttp/websocket"
	"github.com/google/uuid"
	"runtime/debug"
	"time"
)

// WSHandler defines the interface for WebSocket event handling
type WSHandler interface {
	OnConnect(conn WSConnection)
	OnMessage(conn WSConnection, msg []byte)
	OnClose(conn WSConnection)
}

// WSConnection defines the interface for WebSocket connections
type WSConnection interface {
	WriteMessage(messageType int, data []byte) error
	Close() error
	ReadMessage() (messageType int, p []byte, err error)
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
	ID() string
	RemoteAddr() string
}

// wsConnection wraps a websocket connection
type wsConnection struct {
	*websocket.Conn
	id      string
	send    chan []byte
	closeCh chan struct{}
	logger  Logger
}

func newWSConnection(conn *websocket.Conn, logger Logger) *wsConnection {
	if logger == nil {
		logger = NewDefaultLogger() // Use default logger if none provided
	}
	return &wsConnection{
		Conn:    conn,
		id:      uuid.New().String(),
		send:    make(chan []byte, 256),
		closeCh: make(chan struct{}),
		logger:  logger,
	}
}

func (c *wsConnection) ID() string {
	return c.id
}

func (c *wsConnection) RemoteAddr() string {
	return c.Conn.RemoteAddr().String()
}

// readPump handles incoming WebSocket messages
type wsConfig struct {
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	PingInterval time.Duration
	PongWait     time.Duration
	MessageSize  int64
}

func defaultWSConfig() wsConfig {
	return wsConfig{
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 10 * time.Second,
		PingInterval: 54 * time.Second,
		PongWait:     60 * time.Second,
		MessageSize:  512 * 1024, // 512KB
	}
}

func (c *wsConnection) readPump(handler WSHandler) {
	// Use default configuration
	c.readPumpWithConfig(handler, defaultWSConfig())
}

func (c *wsConnection) readPumpWithConfig(handler WSHandler, config wsConfig) {
	defer func() {
		if r := recover(); r != nil {
			c.logger.WithFields(map[string]interface{}{
				"error":     fmt.Sprintf("%v", r),
				"client_id": c.ID(),
				"stack":     string(debug.Stack()),
			}).Error("Panic in WebSocket handler")
		}
		handler.OnClose(c)
		c.Close()
	}()

	c.SetReadLimit(config.MessageSize)
	c.SetReadDeadline(time.Now().Add(config.PongWait))

	c.SetPongHandler(func(string) error {
		c.SetReadDeadline(time.Now().Add(config.PongWait))
		return nil
	})

	for {
		messageType, message, err := c.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseAbnormalClosure,
				websocket.CloseNoStatusReceived) {
				c.logger.WithFields(map[string]interface{}{
					"error":     err,
					"client_id": c.ID(),
				}).Error("WebSocket read error")
			}
			return
		}

		switch messageType {
		case websocket.TextMessage, websocket.BinaryMessage:
			handler.OnMessage(c, message)
		default:
			c.logger.WithFields(map[string]interface{}{
				"type":      messageType,
				"client_id": c.ID(),
			}).Warn("Unsupported WebSocket message type")
		}
	}
}

// writePump handles outgoing WebSocket messages
func (c *wsConnection) writePump() {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		c.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				c.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := c.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.closeCh:
			return
		}
	}
}
