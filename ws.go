package r

import (
	"fmt"
	"github.com/fasthttp/websocket"
	"github.com/google/uuid"
	"golang.org/x/time/rate"
	"sync"
	"sync/atomic"
	"time"
)

// Add as a package-level variable
var (
	defaultConnectionManager *ConnectionManager
	once                     sync.Once
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
		rateLimiter: rate.NewLimiter(rate.Every(time.Millisecond), 1),
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
	rateLimiter   *rate.Limiter // <-- now using a rate.Limiter
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

func (c *wsConnection) ID() string {
	return c.id
}

func (c *wsConnection) RemoteAddr() string {
	return c.Conn.RemoteAddr().String()
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
