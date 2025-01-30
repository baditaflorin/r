// Package r provides a high-performance web framework built on fasthttp
package r

import (
	"bytes"
	"context"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/qiangxue/fasthttp-routing"
	"github.com/valyala/fasthttp"
)

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
	store      map[string]interface{}
	storeMu    sync.Mutex
	requestID  string
	err        error
	aborted    bool
	router     *RouterImpl
}

func newContextImpl(c *routing.Context) *contextImpl {
	ctx, cancel := context.WithCancel(context.Background())
	impl := &contextImpl{
		Context:   c,
		ctx:       ctx,
		cancel:    cancel,
		requestID: uuid.New().String(),
		store:     make(map[string]interface{}),
		handlers:  make([]HandlerFunc, 0),
	}
	return impl
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
	if c.requestID == "" {
		c.requestID = uuid.New().String()
	}
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
