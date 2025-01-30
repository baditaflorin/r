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

func (c *contextImpl) GetRequestID() string {
	return c.requestID
}

func (c *contextImpl) RequestCtx() *fasthttp.RequestCtx {
	return c.Context.RequestCtx
}

func (c *contextImpl) JSON(code int, v interface{}) error {
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

func (c *contextImpl) String(code int, s string) error {
	c.Context.RequestCtx.Response.SetStatusCode(code)
	c.Context.RequestCtx.Response.SetBodyString(s)
	return nil
}

func (c *contextImpl) Next() {
	c.handlerIdx++
	for c.handlerIdx < len(c.handlers) {
		c.handlers[c.handlerIdx](c)
		c.handlerIdx++
	}
}

// WS implements Router.WS
func (r *RouterImpl) WS(path string, handler WSHandler) Router {
	r.group.Get(path, func(c *routing.Context) error {
		var logger Logger
		if l, ok := c.UserValue("logger").(Logger); ok {
			logger = l
		} else {
			logger = NewDefaultLogger()
		}

		// Check if we can accept new connections
		if defaultConnectionManager != nil &&
			defaultConnectionManager.activeConns.Load() >= defaultConnectionManager.maxConns {
			return fmt.Errorf("maximum WebSocket connections reached")
		}

		err := r.upgrader.Upgrade(c.RequestCtx, func(conn *websocket.Conn) {
			wsConn := newWSConnection(conn, logger)
			if wsConn == nil {
				logger.Error("Failed to create WebSocket connection")
				return
			}

			handler.OnConnect(wsConn)

			go wsConn.readPumpWithConfig(handler, defaultWSConfig())
			go wsConn.writePump()
		})

		if err != nil {
			logger.Error("WebSocket upgrade failed",
				"error", err,
				"path", path,
				"remote_addr", c.RemoteIP().String())
			return err
		}
		return nil
	})
	return r
}

// Group implements Router.Group
func (r *RouterImpl) Group(prefix string) Router {
	return &RouterImpl{
		router: r.router,
		group:  r.group.Group(prefix),
	}
}

// Use implements Router.Use
func (r *RouterImpl) Use(middleware ...MiddlewareFunc) Router {
	for _, m := range middleware {
		r.group.Use(func(c *routing.Context) error {
			ctx := newContextImpl(c)
			// Since HandlerFunc and MiddlewareFunc are now the same type,
			// we can directly append the middleware
			ctx.handlers = append(ctx.handlers, m)
			ctx.handlerIdx = -1
			ctx.Next()
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

func newRoutingContext(ctx *fasthttp.RequestCtx) *routing.Context {
	return &routing.Context{
		RequestCtx: ctx,
	}
}
