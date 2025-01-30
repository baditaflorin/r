package r

import (
	"fmt"
	"github.com/fasthttp/websocket"
	"github.com/google/uuid"
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
	id        string
	send      chan []byte
	closeCh   chan struct{}
	logger    Logger
	closeOnce sync.Once
	metrics   MetricsCollector

	// Add connection state tracking
	state    atomic.Int32
	lastPing atomic.Int64
	msgCount atomic.Uint64

	writeBuffer        chan []byte
	rateLimiter        *time.Ticker
	maxBufferSize      int
	dropMessagesOnFull bool
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
		// ReadMessage returns the messageType and message bytes
		_, message, err := c.ReadMessage()
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

		// Get a buffer from the pool
		buf := wsMessagePool.Get().([]byte)
		buf = append(buf[:0], message...) // Copy message into the pooled buffer

		// Handle the message
		handler.OnMessage(c, buf[:len(message)])

		// Return the buffer to the pool
		wsMessagePool.Put(buf)
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
		case <-ticker.C:
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.closeCh:
			return
		default:
			// Try to read from buffer
			select {
			case msg := <-c.writeBuffer:
				if err := c.Conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					c.logger.Error("Failed to write WebSocket message",
						"error", err,
						"conn_id", c.id,
					)
					return
				}

				if c.metrics != nil {
					c.metrics.IncrementCounter("ws.messages.sent", map[string]string{
						"conn_id": c.id,
					})
				}
			}
		}
	}
}

func (c *wsConnection) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.state.Store(wsStateClosing)
		close(c.closeCh)
		err = c.Conn.Close()
		c.state.Store(wsStateClosed)

		// Remove from connection manager
		defaultConnectionManager.Remove(c)

		// Record metrics
		if c.metrics != nil {
			c.metrics.IncrementCounter("ws.connections.closed",
				map[string]string{
					"conn_id": c.id,
				})
			c.metrics.RecordTiming("ws.connection.duration",
				time.Since(time.Unix(0, c.lastPing.Load())),
				map[string]string{
					"conn_id":     c.id,
					"remote_addr": c.RemoteAddr(),
				})
		}
	})
	return err
}

func (c *wsConnection) WriteMessage(messageType int, data []byte) error {
	if c.state.Load() != wsStateActive {
		return ErrConnectionClosed
	}

	select {
	case <-c.rateLimiter.C:
		select {
		case c.writeBuffer <- data:
			if c.metrics != nil {
				c.metrics.IncrementCounter("ws.messages.buffered", map[string]string{
					"conn_id": c.id,
				})
			}
		default:
			if c.dropMessagesOnFull {
				if c.metrics != nil {
					c.metrics.IncrementCounter("ws.messages.dropped", map[string]string{
						"reason":  "buffer_full",
						"conn_id": c.id,
					})
				}
				return ErrBufferFull
			}
			// Wait for space
			select {
			case c.writeBuffer <- data:
				// Message sent
			case <-c.closeCh:
				return ErrConnectionClosed
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
}

func NewConnectionManager(maxConns int32, metrics MetricsCollector, logger Logger) *ConnectionManager {
	if logger == nil {
		logger = NewDefaultLogger()
	}

	cm := &ConnectionManager{
		maxConns: maxConns,
		metrics:  metrics,
		logger:   logger,
	}

	// Start periodic cleanup
	go cm.periodicCleanup()
	return cm
}

func (cm *ConnectionManager) Add(conn *wsConnection) error {
	if cm.activeConns.Load() >= cm.maxConns {
		if cm.metrics != nil {
			cm.metrics.IncrementCounter("ws.connections.rejected",
				map[string]string{"reason": "max_connections_reached"})
		}
		return fmt.Errorf("maximum connections reached")
	}

	cm.connections.Store(conn.ID(), conn)
	count := cm.activeConns.Add(1)

	if cm.metrics != nil {
		cm.metrics.RecordValue("ws.connections.active", float64(count), nil)
		cm.metrics.IncrementCounter("ws.connections.total", nil)
	}

	// Start connection monitoring
	go cm.monitorConnection(conn)
	return nil
}

func (cm *ConnectionManager) Remove(conn *wsConnection) {
	cm.connections.Delete(conn.ID())
	count := cm.activeConns.Add(-1)

	if cm.metrics != nil {
		cm.metrics.RecordValue("ws.connections.active", float64(count), nil)
	}
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
