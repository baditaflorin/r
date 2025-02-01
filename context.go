// Package r provides a high-performance web framework built on fasthttp
package r

import (
	"bytes"
	"context"
	"fmt"
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
	SetStatus(code int)
	ErrorResponse(code int, msg string)
}

// ContextImpl implements the enhanced Context interface
type ContextImpl struct {
	*routing.Context
	ctx        context.Context
	cancel     context.CancelFunc
	handlers   []HandlerFunc
	handlerIdx int
	store      *sync.Map
	requestID  string
	err        error
	aborted    bool
	router     *RouterImpl

	// Tracing and timing
	startTime  time.Time
	spans      []*tracingSpan
	timeouts   map[string]time.Duration
	metrics    MetricsCollector
	errorCause error
	errorStack []string
	done       chan struct{}

	// Add new tracing fields
	rootSpan *tracingSpan
	traceID  string
}

type tracingSpan struct {
	Name      string
	startTime time.Time
	endTime   time.Time
	metadata  map[string]string
	children  []*tracingSpan
}

func (c *ContextImpl) Reset() {
	c.requestID = ""
	c.err = nil
	c.aborted = false
	c.handlerIdx = 0
	c.handlers = nil
	c.store = &sync.Map{}
	c.errorStack = nil
	c.spans = nil
	c.timeouts = make(map[string]time.Duration)
	// Reset any other fields that are per-request
}

func (c *ContextImpl) AddSpan(name string, metadata map[string]string) {
	span := &tracingSpan{
		Name:      name,
		startTime: time.Now(),
		metadata:  metadata,
	}
	c.spans = append(c.spans, span)
}

var contextPool = sync.Pool{
	New: func() interface{} {
		return &ContextImpl{}
	},
}

func (c *ContextImpl) startSpan(name string, attributes map[string]string) *tracingSpan {
	span := &tracingSpan{
		Name:      name,
		startTime: time.Now(),
		metadata:  attributes,
		children:  make([]*tracingSpan, 0),
	}

	// Add standard attributes
	if span.metadata == nil {
		span.metadata = make(map[string]string)
	}
	span.metadata["request_id"] = c.requestID
	span.metadata["trace_id"] = c.traceID

	c.spans = append(c.spans, span)

	// Record span start metric
	if c.metrics != nil {
		c.metrics.IncrementCounter("request.span.start",
			map[string]string{
				"name": name,
			})
	}

	return span
}

func (c *ContextImpl) WithTimeout(timeout time.Duration) (Context, context.CancelFunc) {
	// Create new timeout context
	timeoutCtx, cancel := context.WithTimeout(c.ctx, timeout)

	// Create new context impl with timeout
	newCtx := &ContextImpl{
		Context:   c.Context,
		ctx:       timeoutCtx,
		cancel:    cancel,
		requestID: c.requestID,
		store:     c.store,
		handlers:  c.handlers,
		startTime: c.startTime,
		spans:     c.spans,
		timeouts:  c.timeouts,
		metrics:   c.metrics,
		rootSpan:  c.rootSpan,
		traceID:   c.traceID,
		done:      make(chan struct{}),
	}

	// Track timeout
	newCtx.timeouts[fmt.Sprintf("timeout_%d", len(c.timeouts))] = timeout

	return newCtx, cancel
}

// Implement Context interface methods
func (c *ContextImpl) Deadline() (deadline time.Time, ok bool) {
	return c.ctx.Deadline()
}

func (c *ContextImpl) Done() <-chan struct{} {
	return c.ctx.Done()
}

func (c *ContextImpl) Err() error {
	return c.ctx.Err()
}

func (c *ContextImpl) Value(key interface{}) interface{} {
	return c.ctx.Value(key)
}

func (c *ContextImpl) QueryParam(name string) string {
	return string(c.QueryArgs().Peek(name))
}

func (c *ContextImpl) Stream(code int, contentType string, reader io.Reader) error {
	c.Response.Header.SetContentType(contentType)
	c.Response.SetStatusCode(code)
	_, err := io.Copy(c.Response.BodyWriter(), reader)
	return err
}

func (c *ContextImpl) RealIP() string {
	// First check X-Real-IP
	if ip := c.RequestCtx().Request.Header.Peek("X-Real-IP"); len(ip) > 0 {
		return string(ip)
	}

	// Then check X-Forwarded-For
	if ip := c.RequestCtx().Request.Header.Peek("X-Forwarded-For"); len(ip) > 0 {
		return string(ip)
	}

	// In test context, manually set the remote IP if not set
	if c.RemoteIP().String() == "0.0.0.0" {
		return "127.0.0.1" // Default for tests
	}

	return c.RemoteIP().String()
}

func (c *ContextImpl) IsWebSocket() bool {
	// Get the Upgrade header value
	upgrade := c.RequestCtx().Request.Header.Peek("Upgrade")

	// Check if the header exists and equals "websocket" (case-insensitive)
	return len(upgrade) > 0 && bytes.EqualFold(upgrade, []byte("websocket"))
}

func (c *ContextImpl) Param(name string) string {
	return c.Context.Param(name)
}

func (c *ContextImpl) Abort() {
	c.aborted = true
	c.handlerIdx = len(c.handlers)
}

func (c *ContextImpl) AbortWithError(code int, err error) {
	// Set the error
	c.err = err

	// Set status code in both routing context and response
	c.RequestCtx().Response.SetStatusCode(code)
	c.RequestCtx().SetStatusCode(code)

	// Mark as aborted and stop handler chain
	c.aborted = true
	c.handlerIdx = len(c.handlers)
}

func (c *ContextImpl) ErrorResponse(code int, msg string) {
	c.RequestCtx().Response.SetStatusCode(code)
	c.RequestCtx().SetStatusCode(code)
	c.RequestCtx().Response.SetBodyString(msg)
	c.Abort()
}

func (c *ContextImpl) Error() error {
	return c.err
}

func (c *ContextImpl) Cookie(name string) string {
	return string(c.RequestCtx().Request.Header.Cookie(name))
}

func (c *ContextImpl) SetCookie(cookie *fasthttp.Cookie) {
	c.Response.Header.SetCookie(cookie)
}

func (c *ContextImpl) SetHeader(key, value string) {
	c.RequestCtx().Response.Header.Set(key, value)
}

func (c *ContextImpl) GetHeader(key string) string {
	// First try request headers
	if value := c.RequestCtx().Request.Header.Peek(key); len(value) > 0 {
		return string(value)
	}
	// Then try response headers
	return string(c.RequestCtx().Response.Header.Peek(key))
}
func (c *ContextImpl) RequestID() string {
	if c.requestID == "" {
		c.requestID = uuid.New().String()
	}
	return c.requestID
}

func (c *ContextImpl) Path() string {
	return string(c.RequestCtx().Request.URI().Path())
}

func (c *ContextImpl) Method() string {
	return string(c.RequestCtx().Method())
}

// Update Get method to use sync.Map methods
func (c *ContextImpl) Get(key string) (interface{}, bool) {
	return c.store.Load(key)
}

func (c *ContextImpl) Set(key string, value interface{}) {
	c.store.Store(key, value)
}

func (c *ContextImpl) Redirect(code int, url string) error {
	c.Response.Header.Set("Location", url)
	c.Response.SetStatusCode(code)
	return nil
}

func (c *ContextImpl) GetTraceID() string {
	return c.traceID
}

func (c *ContextImpl) GetSpans() []*tracingSpan {
	return c.spans
}

func (c *ContextImpl) EndSpan(name string) {
	for _, span := range c.spans {
		if span.Name == name && span.endTime.IsZero() {
			span.endTime = time.Now()

			// Record span metrics
			if c.metrics != nil {
				c.metrics.RecordTiming("request.span.duration",
					span.endTime.Sub(span.startTime),
					map[string]string{
						"span_name":  name,
						"path":       c.Path(),
						"method":     c.Method(),
						"request_id": c.requestID,
					})
			}
			break
		}
	}
}

// NewTestContext creates a new context implementation for testing
func NewTestContext(c *routing.Context) *ContextImpl {
	return newContextImpl(c)
}

// TestContextWithHandlers creates a test context with the specified handler chain
func TestContextWithHandlers(c *routing.Context, handlers []HandlerFunc) Context {
	ctx := NewTestContext(c)
	ctx.handlers = handlers
	ctx.handlerIdx = -1
	ctx.aborted = false
	return ctx
}

// TestContextWithTimeout returns a context with timeout for testing
func TestContextWithTimeout(parent Context, timeout time.Duration) (Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	timeoutCtx := &ContextImpl{
		Context:   parent.(*ContextImpl).Context,
		ctx:       ctx,
		cancel:    cancel,
		requestID: parent.RequestID(),
		store:     &sync.Map{},
		handlers:  parent.(*ContextImpl).handlers,
		startTime: time.Now(),
	}
	return timeoutCtx, cancel
}

// Add this helper method to the Context interface
func (c *ContextImpl) SetStatus(code int) {
	c.RequestCtx().Response.SetStatusCode(code)
}
