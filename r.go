// Package r provides a high-performance web framework built on fasthttp
package r

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/url"
	"runtime/debug"
	"sync"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/qiangxue/fasthttp-routing"
	"github.com/valyala/fasthttp"
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
	ID() string         // Added
	RemoteAddr() string // Added
}

// Server defines the interface for the HTTP server
type Server interface {
	Start(address string) error
	Stop() error
}

// HandlerFunc defines a function to serve HTTP requests
type HandlerFunc func(ctx Context)

// MiddlewareFunc defines HTTP middleware
type MiddlewareFunc func(Context)

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
		id:      uuid.New().String(), // You'll need to add "github.com/google/uuid" to your imports
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

// routerImpl implements the Router interface using fasthttp-routing
type routerImpl struct {
	router                  *routing.Router
	group                   *routing.RouteGroup
	upgrader                websocket.FastHTTPUpgrader
	panicHandler            PanicHandlerFunc
	methodNotAllowedHandler HandlerFunc // Add this field
}

func (c *contextImpl) Request() *http.Request {
	// Convert fasthttp.RequestCtx to http.Request
	// This is a simplified version - you might want to add more fields
	r := &http.Request{
		Method: string(c.RequestCtx().Method()),
		URL: &url.URL{
			Path: string(c.RequestCtx().Path()),
		},
	}
	return r
}

func (c *contextImpl) Set(key string, value interface{}) {
	c.storeMu.Lock()
	c.store[key] = value
	c.storeMu.Unlock()
}

func (c *contextImpl) GetRequestID() string {
	return c.requestID
}

func (c *contextImpl) RequestCtx() *fasthttp.RequestCtx {
	return c.Context.RequestCtx
}

func (c *contextImpl) JSON(code int, v interface{}) error {
	c.Context.RequestCtx.Response.Header.SetContentType("application/json")
	c.Context.RequestCtx.Response.SetStatusCode(code)
	return json.NewEncoder(c.Context.RequestCtx.Response.BodyWriter()).Encode(v)
}

func (c *contextImpl) String(code int, s string) error {
	c.Context.RequestCtx.Response.SetStatusCode(code)
	c.Context.RequestCtx.Response.SetBodyString(s)
	return nil
}

func (c *contextImpl) Next() {
	c.handlerIdx++
	if c.handlerIdx < len(c.handlers) {
		c.handlers[c.handlerIdx](c)
	}
}

// WS implements Router.WS
func (r *routerImpl) WS(path string, handler WSHandler) Router {
	r.group.Get(path, func(c *routing.Context) error {
		err := r.upgrader.Upgrade(c.RequestCtx, func(conn *websocket.Conn) {
			wsConn := newWSConnection(conn)
			handler.OnConnect(wsConn)

			go wsConn.readPump(handler)
			go wsConn.writePump()
		})

		if err != nil {
			return err
		}
		return nil
	})
	return r
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

// Group implements Router.Group
func (r *routerImpl) Group(prefix string) Router {
	return &routerImpl{
		router: r.router,
		group:  r.group.Group(prefix),
	}
}

// Use implements Router.Use
func (r *routerImpl) Use(middleware ...MiddlewareFunc) Router {
	for _, m := range middleware {
		r.group.Use(func(c *routing.Context) error {
			ctx := newContextImpl(c)
			m(ctx)
			return nil
		})
	}
	return r
}

var (
	ErrServerClosed = errors.New("server closed")
	ErrTimeout      = errors.New("timeout")
	ErrInvalidJSON  = errors.New("invalid JSON")
)

// Config holds server configuration
type Config struct {
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	IdleTimeout        time.Duration
	MaxRequestBodySize int
	CertFile           string
	KeyFile            string
	TLSConfig          *tls.Config
	WSConfig           WSConfig
	Handler            Router // Add this field
}

// WSConfig holds WebSocket-specific configuration
type WSConfig struct {
	HandshakeTimeout  time.Duration
	ReadBufferSize    int
	WriteBufferSize   int
	EnableCompression bool
	Origins           []string
	Path              string
}

// DefaultConfig returns a Config with sensible defaults
func DefaultConfig() Config {
	return Config{
		ReadTimeout:        15 * time.Second,
		WriteTimeout:       15 * time.Second,
		IdleTimeout:        60 * time.Second,
		MaxRequestBodySize: 4 << 20, // 4MB
		WSConfig: WSConfig{
			HandshakeTimeout:  10 * time.Second,
			ReadBufferSize:    4096,
			WriteBufferSize:   4096,
			EnableCompression: true,
		},
	}
}

// Context represents the enhanced request context
type Context interface {
	context.Context
	RequestCtx() *fasthttp.RequestCtx
	Param(name string) string
	QueryParam(name string) string
	JSON(code int, v interface{}) error
	String(code int, s string) error
	Stream(code int, contentType string, reader io.Reader) error
	Redirect(code int, url string) error
	SetHeader(key, value string)
	GetHeader(key string) string
	Cookie(name string) string
	SetCookie(cookie *fasthttp.Cookie)
	RequestID() string
	RealIP() string
	Path() string
	Method() string
	IsWebSocket() bool
	Next()
	Abort()
	AbortWithError(code int, err error)
	Error() error
	Set(key string, value interface{})
	Get(key string) (interface{}, bool)
}

// contextImpl implements the enhanced Context interface
type contextImpl struct {
	*routing.Context
	ctx        context.Context
	cancel     context.CancelFunc
	handlers   []HandlerFunc
	handlerIdx int
	store      map[string]interface{} // Changed from sync.Map
	storeMu    sync.Mutex             // Added mutex for store
	requestID  string
	err        error
	aborted    bool
	router     *routerImpl
}

func newContextImpl(c *routing.Context) *contextImpl {
	ctx, cancel := context.WithCancel(context.Background())
	return &contextImpl{
		Context:   c,
		ctx:       ctx,
		cancel:    cancel,
		requestID: uuid.New().String(),
		store:     make(map[string]interface{}), // Initialize map
	}
}

// Implement Context interface methods
func (c *contextImpl) Deadline() (deadline time.Time, ok bool) {
	return c.ctx.Deadline()
}

func (c *contextImpl) Done() <-chan struct{} {
	return c.ctx.Done()
}

func (c *contextImpl) Err() error {
	return c.ctx.Err()
}

func (c *contextImpl) Value(key interface{}) interface{} {
	return c.ctx.Value(key)
}

func (c *contextImpl) QueryParam(name string) string {
	return string(c.QueryArgs().Peek(name))
}

func (c *contextImpl) Stream(code int, contentType string, reader io.Reader) error {
	c.Response.Header.SetContentType(contentType)
	c.Response.SetStatusCode(code)
	_, err := io.Copy(c.Response.BodyWriter(), reader)
	return err
}

func (c *contextImpl) RealIP() string {
	// Use RequestCtx() instead of Request
	if ip := c.RequestCtx().Request.Header.Peek("X-Real-IP"); len(ip) > 0 {
		return string(ip)
	}
	if ip := c.RequestCtx().Request.Header.Peek("X-Forwarded-For"); len(ip) > 0 {
		return string(ip)
	}
	return c.RemoteIP().String()
}

func (c *contextImpl) IsWebSocket() bool {
	return bytes.Equal(c.RequestCtx().Request.Header.Peek("Upgrade"), []byte("websocket"))
}

func (c *contextImpl) Abort() {
	c.aborted = true
}

func (c *contextImpl) AbortWithError(code int, err error) {
	c.err = err
	c.Response.SetStatusCode(code)
	c.Abort()
}

func (c *contextImpl) Error() error {
	return c.err
}

// Enhanced Router implementation
type Router interface {
	GET(path string, handlers ...HandlerFunc) Router
	POST(path string, handlers ...HandlerFunc) Router
	PUT(path string, handlers ...HandlerFunc) Router
	DELETE(path string, handlers ...HandlerFunc) Router
	PATCH(path string, handlers ...HandlerFunc) Router
	HEAD(path string, handlers ...HandlerFunc) Router
	OPTIONS(path string, handlers ...HandlerFunc) Router
	WS(path string, handler WSHandler) Router
	Group(prefix string) Router
	Use(middleware ...MiddlewareFunc) Router
	Static(prefix, root string) Router
	FileServer(path, root string) Router
	NotFound(handler HandlerFunc)
	MethodNotAllowed(handler HandlerFunc)
	PanicHandler(handler PanicHandlerFunc)
}

type PanicHandlerFunc func(Context, interface{})

// Recovery middleware
func Recovery() MiddlewareFunc {
	return func(c Context) {
		defer func() {
			if rcv := recover(); rcv != nil {
				if h := c.(*contextImpl).router.panicHandler; h != nil {
					h(c, rcv)
				} else {
					c.AbortWithError(http.StatusInternalServerError, fmt.Errorf("panic recovered: %v", rcv))
				}
			}
		}()
		c.Next()
	}
}

// RequestID middleware
func RequestID() MiddlewareFunc {
	return func(c Context) {
		c.SetHeader("X-Request-ID", c.RequestID())
		c.Next()
	}
}

// CORS middleware
func CORS(origins []string) MiddlewareFunc {
	return func(c Context) {
		origin := string(c.RequestCtx().Request.Header.Peek("Origin"))

		// Check if origin is allowed
		allowed := false
		for _, o := range origins {
			if o == "*" || o == origin {
				allowed = true
				break
			}
		}

		if allowed {
			c.SetHeader("Access-Control-Allow-Origin", origin)
			c.SetHeader("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
			c.SetHeader("Access-Control-Allow-Headers", "Authorization,Content-Type")
			c.SetHeader("Access-Control-Allow-Credentials", "true")

			// Handle preflight requests
			if c.Method() == "OPTIONS" {
				c.AbortWithError(http.StatusNoContent, nil)
				return
			}
		}

		c.Next()
	}
}

// Implement all required Context methods
func (c *contextImpl) Cookie(name string) string {
	return string(c.RequestCtx().Request.Header.Cookie(name))
}

func (c *contextImpl) SetCookie(cookie *fasthttp.Cookie) {
	c.Response.Header.SetCookie(cookie)
}

func (c *contextImpl) SetHeader(key, value string) {
	c.Response.Header.Set(key, value)
}

func (c *contextImpl) GetHeader(key string) string {
	return string(c.RequestCtx().Request.Header.Peek(key))
}

func (c *contextImpl) RequestID() string {
	return c.requestID
}

func (c *contextImpl) Path() string {
	return string(c.RequestCtx().Request.URI().Path())
}

func (c *contextImpl) Method() string {
	return string(c.RequestCtx().Method())
}

func (c *contextImpl) Get(key string) (interface{}, bool) {
	c.storeMu.Lock()
	value, exists := c.store[key]
	c.storeMu.Unlock()
	return value, exists
}

func (c *contextImpl) Redirect(code int, url string) error {
	c.Response.Header.Set("Location", url)
	c.Response.SetStatusCode(code)
	return nil
}

// Implement all required Router methods
func (r *routerImpl) DELETE(path string, handlers ...HandlerFunc) Router {
	r.group.Delete(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *routerImpl) PUT(path string, handlers ...HandlerFunc) Router {
	r.group.Put(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *routerImpl) PATCH(path string, handlers ...HandlerFunc) Router {
	r.group.Patch(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *routerImpl) HEAD(path string, handlers ...HandlerFunc) Router {
	r.group.Head(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *routerImpl) OPTIONS(path string, handlers ...HandlerFunc) Router {
	r.group.Options(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *routerImpl) Static(prefix, root string) Router {
	r.group.Get(prefix+"/*", staticHandler(root))
	return r
}

func (r *routerImpl) FileServer(path, root string) Router {
	r.group.Get(path, staticHandler(root))
	return r
}

func (r *routerImpl) NotFound(handler HandlerFunc) {
	r.router.NotFound(r.wrapHandlers(handler)[0])
}

func (r *routerImpl) MethodNotAllowed(handler HandlerFunc) {
	// Store the handler to be used in NotFound
	r.methodNotAllowedHandler = handler

	// Set up a NotFound handler that will check if the method is not allowed
	r.router.NotFound(func(c *routing.Context) error {
		// Create our context implementation
		ctx := newContextImpl(c)

		// Call the method not allowed handler
		if r.methodNotAllowedHandler != nil {
			r.methodNotAllowedHandler(ctx)
		}
		return nil
	})
}

func (r *routerImpl) PanicHandler(handler PanicHandlerFunc) {
	r.panicHandler = handler
}

// Helper method to wrap HandlerFunc into routing.Handler
func (r *routerImpl) wrapHandlers(handlers ...HandlerFunc) []routing.Handler {
	wrapped := make([]routing.Handler, len(handlers))
	for i, h := range handlers {
		h := h // Create a new variable scope
		wrapped[i] = func(c *routing.Context) error {
			ctx := &contextImpl{
				Context:  c,
				router:   r,
				handlers: handlers,
			}
			h(ctx)
			return nil
		}
	}
	return wrapped
}

func staticHandler(root string) routing.Handler {
	fs := &fasthttp.FS{
		Root:            root,
		IndexNames:      []string{"index.html"},
		Compress:        true,
		CompressBrotli:  true,
		AcceptByteRange: true,
	}
	handler := fs.NewRequestHandler()

	return func(c *routing.Context) error {
		handler(c.RequestCtx)
		return nil
	}
}

// Update existing Router methods to use wrapHandlers
func (r *routerImpl) GET(path string, handlers ...HandlerFunc) Router {
	r.group.Get(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *routerImpl) POST(path string, handlers ...HandlerFunc) Router {
	r.group.Post(path, r.wrapHandlers(handlers...)...)
	return r
}

// NewRouter creates a new Router instance
func NewRouter() Router {
	r := &routerImpl{
		router: routing.New(),
		upgrader: websocket.FastHTTPUpgrader{
			EnableCompression: true,
			CheckOrigin: func(ctx *fasthttp.RequestCtx) bool {
				return true
			},
		},
	}
	r.group = r.router.Group("")
	return r
}

// Server implementation
type serverImpl struct {
	server   *fasthttp.Server
	router   *routerImpl
	config   Config
	shutdown chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
}

func NewServer(config Config) *serverImpl {
	if config.Handler == nil {
		config.Handler = NewRouter()
	}

	s := &serverImpl{
		router:   config.Handler.(*routerImpl), // Use the provided router
		config:   config,
		shutdown: make(chan struct{}),
	}

	// Set up default panic handler
	s.router.PanicHandler(func(c Context, rcv interface{}) {
		err := fmt.Errorf("panic: %v\n%s", rcv, debug.Stack())
		c.AbortWithError(http.StatusInternalServerError, err)
	})

	return s
}

func (s *serverImpl) buildHandler() fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		s.router.router.HandleRequest(ctx)
	}
}

func (s *serverImpl) Start(address string) error {
	s.server = &fasthttp.Server{
		Handler:            s.buildHandler(),
		ReadTimeout:        s.config.ReadTimeout,
		WriteTimeout:       s.config.WriteTimeout,
		IdleTimeout:        s.config.IdleTimeout,
		MaxRequestBodySize: s.config.MaxRequestBodySize,
		// MaxHeaderBytes removed as it's not supported by fasthttp
	}

	if s.config.CertFile != "" && s.config.KeyFile != "" {
		return s.server.ListenAndServeTLS(address, s.config.CertFile, s.config.KeyFile)
	}
	return s.server.ListenAndServe(address)
}

func (s *serverImpl) Stop() error {
	s.mu.Lock()
	close(s.shutdown)
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return s.server.ShutdownWithContext(ctx)
}
