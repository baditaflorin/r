// Package r provides a high-performance web framework built on fasthttp
package r

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/qiangxue/fasthttp-routing"
	"github.com/valyala/fasthttp"
)

// Add a pool for JSON encoding buffers at the top of log.go
var jsonBufferPool = sync.Pool{
	New: func() interface{} { return new(bytes.Buffer) },
}

type ConfigProvider interface {
	GetConfig() Config
	SetConfig(Config) error
	LoadFromFile(path string) error
	LoadFromEnv() error
}

type RouterProvider interface {
	Router
	WithMiddlewareProvider(provider MiddlewareProvider) RouterProvider
	WithErrorHandler(handler ErrorHandler) RouterProvider
	WithWSUpgrader(upgrader WSUpgrader) RouterProvider
}

type ContextFactory interface {
	CreateContext(*fasthttp.RequestCtx) Context
	WithStore(store Store) ContextFactory
}

type Store interface {
	Get(key string) (interface{}, bool)
	Set(key string, value interface{})
	Delete(key string)
	Clear()
}

type WSUpgrader interface {
	Upgrade(*fasthttp.RequestCtx, WSHandler) (WSConnection, error)
	WithConfig(WSConfig) WSUpgrader
}

// 8. Error Handler Interface (new)
type ErrorHandler interface {
	HandleError(Context, error)
	HandlePanic(Context, interface{})
	WithLogger(Logger) ErrorHandler
}

// 10. Health Checker Interface (new)
type HealthChecker interface {
	Check() error
	RegisterCheck(name string, check func() error)
	Start(context.Context) error
	Stop() error
}

// HandlerFunc defines a function to serve HTTP requests
type HandlerFunc = MiddlewareFunc

// MiddlewareFunc defines HTTP middleware
type MiddlewareFunc func(Context)

func (c *ContextImpl) Request() *http.Request {
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

func (c *ContextImpl) GetRequestID() string {
	return c.requestID
}

func (c *ContextImpl) RequestCtx() *fasthttp.RequestCtx {
	return c.Context.RequestCtx
}

func (c *ContextImpl) JSON(code int, v interface{}) error {
	c.Context.RequestCtx.Response.Header.SetContentType("application/json")
	c.Context.RequestCtx.Response.SetStatusCode(code)

	// Get a buffer from the pool
	buf := jsonBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer jsonBufferPool.Put(buf) // Return buffer to the pool

	// Encode the JSON directly into the buffer
	if err := json.NewEncoder(buf).Encode(v); err != nil {
		return err
	}

	// Write the bytes to the response
	c.Context.RequestCtx.Response.SetBody(buf.Bytes())
	return nil
}

func (c *ContextImpl) String(code int, s string) error {
	c.Context.RequestCtx.Response.SetStatusCode(code)
	c.Context.RequestCtx.Response.SetBodyString(s)
	return nil
}

func (c *ContextImpl) Next() {
	c.handlerIdx++
	for c.handlerIdx < len(c.handlers) && !c.aborted {
		c.handlers[c.handlerIdx](c)
		c.handlerIdx++
	}
}

// WS implements Router.WS
// File: router.go

// Update the WS method in RouterImpl to properly handle WebSocket connections
func (r *RouterImpl) WS(path string, handler WSHandler) Router {
	if handler == nil {
		panic("WebSocket handler cannot be nil")
	}

	r.group.Get(path, func(c *routing.Context) error {
		upgrader := websocket.FastHTTPUpgrader{
			ReadBufferSize:    1024,
			WriteBufferSize:   1024,
			HandshakeTimeout:  10 * time.Second,
			EnableCompression: true,
			CheckOrigin: func(ctx *fasthttp.RequestCtx) bool {
				return true // You might want to make this configurable
			},
		}

		err := upgrader.Upgrade(c.RequestCtx, func(ws *websocket.Conn) {
			wsConn := NewWSConn(ws, r.logger, handler)
			defer wsConn.Close()

			// Block here until the connection is closed
			<-wsConn.closeCh
		})

		if err != nil {
			return fmt.Errorf("websocket upgrade failed: %v", err)
		}

		return nil
	})

	return r
}

// Group implements Router.Group
func (r *RouterImpl) Group(prefix string) Router {
	newGroup := r.group.Group(prefix)
	newRouter := &RouterImpl{
		router:       r.router,
		group:        newGroup,
		middleware:   make([]HandlerFunc, len(r.middleware)),
		routes:       make(map[string]*Route),
		routeMetrics: r.routeMetrics,
		upgrader:     r.upgrader,
	}

	// Copy middleware to the new group
	copy(newRouter.middleware, r.middleware)

	return newRouter
}

// Use implements Router.Use
func (r *RouterImpl) Use(middleware ...MiddlewareFunc) Router {
	// Store middleware in our slice
	r.middleware = append(r.middleware, middleware...)

	// Add to the underlying route group
	for _, m := range middleware {
		m := m // Capture for closure
		r.group.Use(func(c *routing.Context) error {
			ctx := newContextImpl(c)
			m(ctx)
			return ctx.Error()
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

func newRoutingContext(ctx *fasthttp.RequestCtx) *routing.Context {
	return &routing.Context{
		RequestCtx: ctx,
	}
}
