package r

import (
	"fmt"
	"github.com/fasthttp/websocket"
	"github.com/google/uuid"
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
}

func newWSConnection(conn *websocket.Conn) *wsConnection {
	return &wsConnection{
		Conn:    conn,
		id:      uuid.New().String(),
		send:    make(chan []byte, 256),
		closeCh: make(chan struct{}),
	}
}

func (c *wsConnection) ID() string {
	return c.id
}

func (c *wsConnection) RemoteAddr() string {
	return c.Conn.RemoteAddr().String()
}

// readPump handles incoming WebSocket messages
func (c *wsConnection) readPump(handler WSHandler) {
	defer func() {
		handler.OnClose(c)
		c.Close()
	}()

	c.SetReadDeadline(time.Now().Add(60 * time.Second))
	for {
		_, message, err := c.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				fmt.Printf("ws error: %v\n", err)
			}
			break
		}
		handler.OnMessage(c, message)
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
