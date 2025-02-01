package r

import (
	"bytes"
	"fmt"
	"github.com/fasthttp/websocket"
	"github.com/google/uuid"
	"io"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// Add as a package-level variable
var (
	defaultConnectionManager *ConnectionManager
	once                     sync.Once
)

// Add a sync.Pool for WebSocket message buffers at the top of ws.go
var wsMessagePool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 64*1024) // 64KB buffer for WebSocket messages
	},
}

// Initialize the message pool (add this global variable at the top of ws.go)
var (
	messagePool = sync.Pool{
		New: func() interface{} {
			// Preallocate buffers of maximum expected message size
			return make([]byte, 512*1024) // 512KB
		},
	}
)

// Update NewWSConn to properly initialize the connection
func NewWSConn(conn *websocket.Conn, logger Logger, handler WSHandler) *wsConnection {
	if logger == nil {
		logger = NewDefaultLogger()
	}

	// Initialize the connection manager if needed.
	once.Do(func() {
		defaultConnectionManager = NewConnectionManager(
			10000, // max connections
			NewDefaultMetricsCollector(),
			logger,
		)
	})

	wsConn := &wsConnection{
		Conn:        conn,
		id:          uuid.New().String(),
		send:        make(chan []byte, 256),
		closeCh:     make(chan struct{}),
		logger:      logger,
		handler:     handler,
		rateLimiter: time.NewTicker(time.Millisecond),
	}
	wsConn.state.Store(wsStateActive)
	wsConn.lastPing.Store(time.Now().UnixNano())
	wsConn.writeBuffer = make(chan []byte, 1024)

	// Add the connection to the manager.
	if defaultConnectionManager != nil {
		if err := defaultConnectionManager.Add(wsConn); err != nil {
			logger.Error("Failed to add connection", "error", err, "conn_id", wsConn.id)
			conn.Close()
			return nil
		}
	}

	go wsConn.readPump()
	go wsConn.writePump()

	if handler != nil {
		handler.OnConnect(wsConn)
	}

	return wsConn
}

func (c *wsConnection) readPumpWithHandler(handler WSHandler) {
	if c.logger == nil {
		c.logger = NewDefaultLogger()
	}

	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("Panic in WebSocket handler",
				"error", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()),
				"conn_id", c.id)
		}
		handler.OnClose(c)
		c.Close()
	}()

	c.SetReadLimit(32 * 1024)
	c.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.SetPongHandler(func(string) error {
		c.SetReadDeadline(time.Now().Add(60 * time.Second))
		c.lastPing.Store(time.Now().UnixNano())
		return nil
	})

	for {
		messageType, message, err := c.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseAbnormalClosure) {
				c.logger.Error("WebSocket read error",
					"error", err,
					"conn_id", c.id)
			}
			return
		}

		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}

		handler.OnMessage(c, message)
	}
}

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
	CloseCode() (int, string)
}

// wsConnection wraps a websocket connection
type wsConnection struct {
	*websocket.Conn
	id        string
	send      chan []byte
	closeCh   chan struct{}
	logger    Logger
	closeOnce sync.Once
	metrics   MetricsCollector
	mu        sync.Mutex

	// Add connection state tracking
	state    atomic.Int32
	lastPing atomic.Int64
	msgCount atomic.Uint64

	writeBuffer   chan []byte
	rateLimiter   *time.Ticker
	readDeadline  time.Duration
	writeDeadline time.Duration

	maxBufferSize    int
	maxMessageSize   int64
	compressionLevel int

	dropMessagesOnFull bool

	handler          WSHandler // Add handler reference
	messageValidator func([]byte) error

	closeCode   int    // Add this field
	closeReason string // Add this field

}

const (
	wsStateActive  = 0
	wsStateClosing = 1
	wsStateClosed  = 2
)

// Update the existing newWSConnection function to use connection manager
func newWSConnection(conn *websocket.Conn, logger Logger) *wsConnection {
	// Initialize the connection manager if not already done
	once.Do(func() {
		defaultConnectionManager = NewConnectionManager(
			10000, // Default max connections
			NewDefaultMetricsCollector(),
			logger,
		)
	})

	wsConn := &wsConnection{
		Conn:               conn,
		id:                 uuid.New().String(),
		send:               make(chan []byte, 256),
		closeCh:            make(chan struct{}),
		logger:             logger,
		writeBuffer:        make(chan []byte, 1024),
		rateLimiter:        time.NewTicker(time.Millisecond),
		maxBufferSize:      1024,
		dropMessagesOnFull: true,
	}

	// Add the connection to the manager
	if err := defaultConnectionManager.Add(wsConn); err != nil {
		if logger != nil {
			logger.Error("Failed to add connection",
				"error", err,
				"conn_id", wsConn.id)
		}
		conn.Close()
		return nil
	}

	return wsConn
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

func (c *wsConnection) readPump() {
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("Panic in WebSocket read pump",
				"error", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()),
				"conn_id", c.id)
		}
		c.Close()
	}()

	c.SetReadLimit(10 * 1024 * 1024)                   // 10MB max message size
	c.SetReadDeadline(time.Now().Add(5 * time.Second)) // Reduced from 60s to 5s

	// Update close handler to capture close code
	c.SetCloseHandler(func(code int, text string) error {
		c.mu.Lock()
		c.closeCode = code
		c.closeReason = text
		c.mu.Unlock()
		c.state.Store(wsStateClosed)
		return nil
	})

	c.SetPongHandler(func(string) error {
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		return nil
	})

	for {
		messageType, reader, err := c.NextReader()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseAbnormalClosure) {
				c.logger.Error("WebSocket read error",
					"error", err,
					"conn_id", c.id)
			}
			return
		}

		// Set up a limited reader to prevent memory exhaustion
		limitedReader := io.LimitReader(reader, 10*1024*1024+1) // 10MB + 1 byte to detect overflow

		// Use a buffer to read the message
		buf := &bytes.Buffer{}
		written, err := io.Copy(buf, limitedReader)

		if err != nil {
			c.logger.Error("Failed to read message",
				"error", err,
				"conn_id", c.id)
			c.handleMessageError("Failed to read message")
			return
		}

		// Check if message exceeds size limit
		if written > 10*1024*1024 {
			c.logger.Error("Message size exceeds limit",
				"size", written,
				"conn_id", c.id)
			c.handleMessageError("Message size exceeds maximum allowed size of 10MB")
			return
		}

		// Process valid message
		if messageType == websocket.TextMessage || messageType == websocket.BinaryMessage {
			message := buf.Bytes()
			msgCopy := make([]byte, len(message))
			copy(msgCopy, message)

			if c.handler != nil {
				c.handler.OnMessage(c, msgCopy)
			}
			c.msgCount.Add(1)
		}
	}
}

func (c *wsConnection) handleMessageError(msg string) {
	// Send error message to client
	closeMsg := websocket.FormatCloseMessage(
		websocket.CloseMessageTooBig,
		msg,
	)
	c.WriteControl(websocket.CloseMessage, closeMsg, time.Now().Add(time.Second))

	// Notify handler of error if implemented
	if errHandler, ok := c.handler.(WSErrorHandler); ok {
		errHandler.OnError(c, fmt.Errorf(msg))
	}

	// Close the connection
	c.Close()
}

type WSErrorHandler interface {
	OnError(conn WSConnection, err error)
}

func (c *wsConnection) writePump() {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		if c.rateLimiter != nil {
			c.rateLimiter.Stop()
		}
		c.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				// Channel was closed
				err := c.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
					time.Now().Add(time.Second),
				)
				if err != nil {
					c.logger.Error("Error sending close message",
						"error", err,
						"conn_id", c.id)
				}
				return
			}

			c.SetWriteDeadline(time.Now().Add(10 * time.Second))
			err := c.WriteMessage(websocket.TextMessage, message)
			if err != nil {
				c.logger.Error("Write error",
					"error", err,
					"conn_id", c.id)
				return
			}

		case <-ticker.C:
			c.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-c.closeCh:
			return
		}
	}
}

func (c *wsConnection) Close() error {
	var err error
	c.closeOnce.Do(func() {
		// Mark connection as closing
		c.state.Store(wsStateClosing)

		// Notify handler before closing
		if c.handler != nil {
			c.handler.OnClose(c)
		}

		// Close the close channel to stop pumps
		close(c.closeCh)

		// Close the underlying connection
		err = c.Conn.Close()

		// Mark as fully closed
		c.state.Store(wsStateClosed)

		// Remove the connection from the manager so that stats update correctly.
		if defaultConnectionManager != nil {
			defaultConnectionManager.Remove(c)
		}
	})
	return err
}

func (c *wsConnection) WriteMessage(messageType int, data []byte) error {
	if c == nil || c.Conn == nil {
		return fmt.Errorf("connection is nil")
	}
	if c.state.Load() != wsStateActive {
		return ErrConnectionClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeBuffer == nil || c.rateLimiter == nil {
		return fmt.Errorf("connection not properly initialized")
	}

	// Try sending the message with a short timeout before giving up.
	select {
	case <-c.rateLimiter.C:
		select {
		case c.writeBuffer <- data:
			// Successfully queued message.
		case <-time.After(10 * time.Millisecond): // wait a little bit
			if c.dropMessagesOnFull {
				if c.metrics != nil {
					c.metrics.IncrementCounter("ws.messages.dropped", map[string]string{
						"reason":  "buffer_full",
						"conn_id": c.id,
					})
				}
				return ErrBufferFull
			}
		}
		if c.metrics != nil {
			c.metrics.IncrementCounter("ws.messages.buffered", map[string]string{
				"conn_id": c.id,
			})
		}
		return nil
	default:
		if c.metrics != nil {
			c.metrics.IncrementCounter("ws.messages.dropped", map[string]string{
				"reason":  "rate_limited",
				"conn_id": c.id,
			})
		}
		return ErrRateLimited
	}
}

var (
	ErrBufferFull       = fmt.Errorf("message buffer is full")
	ErrConnectionClosed = fmt.Errorf("connection is closed")
	ErrRateLimited      = fmt.Errorf("rate limit exceeded")
)

// MessageBuffer implements a fixed-size circular buffer for WebSocket messages
type MessageBuffer struct {
	buffer   [][]byte
	size     int
	head     int
	tail     int
	count    int
	mu       sync.Mutex
	notFull  chan struct{}
	notEmpty chan struct{}
}

func NewMessageBuffer(size int) *MessageBuffer {
	return &MessageBuffer{
		buffer:   make([][]byte, size),
		size:     size,
		notFull:  make(chan struct{}, 1),
		notEmpty: make(chan struct{}, 1),
	}
}

func (b *MessageBuffer) Write(data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.count == b.size {
		return ErrBufferFull
	}

	// Make a copy of the data to prevent race conditions
	msg := make([]byte, len(data))
	copy(msg, data)

	b.buffer[b.tail] = msg
	b.tail = (b.tail + 1) % b.size
	b.count++

	// Signal that buffer is not empty
	select {
	case b.notEmpty <- struct{}{}:
	default:
	}

	return nil
}

func (b *MessageBuffer) Read() ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.count == 0 {
		return nil, nil
	}

	data := b.buffer[b.head]
	b.buffer[b.head] = nil // Allow GC to reclaim the memory
	b.head = (b.head + 1) % b.size
	b.count--

	// Signal that buffer is not full
	select {
	case b.notFull <- struct{}{}:
	default:
	}

	return data, nil
}

func (b *MessageBuffer) NotFull() <-chan struct{} {
	return b.notFull
}

func (b *MessageBuffer) NotEmpty() <-chan struct{} {
	return b.notEmpty
}

func (b *MessageBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

type ConnectionManager struct {
	connections sync.Map
	metrics     MetricsCollector
	logger      Logger
	maxConns    int32
	activeConns atomic.Int32
	mu          sync.RWMutex
}

func NewConnectionManager(maxConns int32, metrics MetricsCollector, logger Logger) *ConnectionManager {
	return &ConnectionManager{
		maxConns: maxConns,
		metrics:  metrics,
		logger:   logger,
	}
}

func (cm *ConnectionManager) Add(conn *wsConnection) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	currentConns := cm.activeConns.Load()
	if currentConns >= cm.maxConns {
		return fmt.Errorf("connection limit reached")
	}

	// Add connection first
	cm.connections.Store(conn.ID(), conn)

	// Then increment counter
	cm.activeConns.Add(1)

	if cm.metrics != nil {
		cm.metrics.RecordValue("ws.connections.active",
			float64(cm.activeConns.Load()), nil)
	}

	return nil
}

func (cm *ConnectionManager) Remove(conn *wsConnection) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if _, exists := cm.connections.LoadAndDelete(conn.ID()); exists {
		newCount := cm.activeConns.Add(-1)
		if cm.metrics != nil {
			cm.metrics.RecordValue("ws.connections.active",
				float64(newCount), nil)
		}
	}
}

func (cm *ConnectionManager) GetActiveConnections() int32 {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.activeConns.Load()
}

func (cm *ConnectionManager) monitorConnection(conn *wsConnection) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Check connection health
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				cm.logger.Error("Connection health check failed",
					"conn_id", conn.ID(),
					"error", err)
				conn.Close()
				cm.Remove(conn)
				return
			}
		case <-conn.closeCh:
			cm.Remove(conn)
			return
		}
	}
}

func (cm *ConnectionManager) periodicCleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		staleCount := 0
		cm.connections.Range(func(key, value interface{}) bool {
			conn := value.(*wsConnection)
			if time.Since(time.Unix(0, conn.lastPing.Load())) > 10*time.Minute {
				cm.logger.Warn("Removing stale connection",
					"conn_id", conn.ID(),
					"last_ping", time.Unix(0, conn.lastPing.Load()))
				conn.Close()
				cm.Remove(conn)
				staleCount++
			}
			return true
		})

		if cm.metrics != nil && staleCount > 0 {
			cm.metrics.IncrementCounter("ws.connections.cleaned",
				map[string]string{"count": fmt.Sprintf("%d", staleCount)})
		}
	}
}

func ConfigureConnectionManager(maxConns int32, metrics MetricsCollector, logger Logger) {
	if logger == nil {
		logger = NewDefaultLogger()
	}

	once.Do(func() {
		defaultConnectionManager = NewConnectionManager(maxConns, metrics, logger)
	})
}

func GetConnectionStats() map[string]interface{} {
	if defaultConnectionManager == nil {
		return nil
	}

	stats := map[string]interface{}{
		"active_connections": defaultConnectionManager.activeConns.Load(),
		"max_connections":    defaultConnectionManager.maxConns,
	}

	// Count connections by state
	stateCount := make(map[string]int)
	defaultConnectionManager.connections.Range(func(_, value interface{}) bool {
		conn := value.(*wsConnection)
		state := "unknown"
		switch conn.state.Load() {
		case wsStateActive:
			state = "active"
		case wsStateClosing:
			state = "closing"
		case wsStateClosed:
			state = "closed"
		}
		stateCount[state]++
		return true
	})
	stats["connections_by_state"] = stateCount

	return stats
}

func (c *wsConnection) CloseCode() (int, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCode, c.closeReason
}
