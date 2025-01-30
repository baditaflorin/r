// Package r provides a high-performance web framework built on fasthttp
package r

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"runtime/debug"
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
	name      string
	startTime time.Time
	endTime   time.Time
	metadata  map[string]string
	children  []*tracingSpan
}

func (c *contextImpl) AddSpan(name string, metadata map[string]string) {
	span := &tracingSpan{
		name:      name,
		startTime: time.Now(),
		metadata:  metadata,
	}
	c.spans = append(c.spans, span)
}

var contextPool = sync.Pool{
	New: func() interface{} {
		return &contextImpl{}
	},
}

func newContextImpl(c *routing.Context) *contextImpl {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	impl := contextPool.Get().(*contextImpl)
	impl.Context = c
	impl.ctx = ctx
	impl.cancel = cancel
	impl.requestID = uuid.New().String()
	impl.store = &sync.Map{}
	impl.handlers = nil
	impl.startTime = time.Now()
	impl.spans = nil
	impl.timeouts = make(map[string]time.Duration)
	impl.done = make(chan struct{})
	impl.errorStack = nil
	impl.traceID = uuid.New().String()
	impl.rootSpan = impl.startSpan("request", map[string]string{
		"request_id": impl.requestID,
		"method":     impl.Method(),
		"path":       impl.Path(),
		"remote_ip":  impl.RealIP(),
		"trace_id":   impl.traceID,
	})

	// Ensure cleanup on context done
	go func() {
		<-ctx.Done()
		impl.cleanup()
		contextPool.Put(impl) // Return to pool for reuse
	}()

	return impl
}

func (c *contextImpl) cleanup() {
	// Ensure cancel is called
	if c.cancel != nil {
		c.cancel()
	}

	// End all spans
	if c.rootSpan != nil {
		c.rootSpan.endTime = time.Now()

		// Record root span metrics
		if c.metrics != nil {
			c.metrics.RecordTiming("request.total_time",
				c.rootSpan.endTime.Sub(c.rootSpan.startTime),
				map[string]string{
					"path":       c.Path(),
					"method":     c.Method(),
					"request_id": c.requestID,
					"span":       "root",
				})
		}
	}

	// End any remaining open spans
	for _, span := range c.spans {
		if span.endTime.IsZero() {
			span.endTime = time.Now()
		}
	}

	// Record final metrics
	if c.metrics != nil {
		c.metrics.RecordTiming("request.total_time",
			time.Since(c.startTime),
			map[string]string{
				"path":       c.Path(),
				"method":     c.Method(),
				"request_id": c.requestID,
			})

		if err := c.Error(); err != nil {
			c.metrics.IncrementCounter("request.errors",
				map[string]string{
					"path":   c.Path(),
					"method": c.Method(),
					"error":  err.Error(),
				})
		}
	}

	// Close done channel
	select {
	case <-c.done:
	default:
		close(c.done)
	}
}

func (c *contextImpl) startSpan(name string, attributes map[string]string) *tracingSpan {
	span := &tracingSpan{
		name:      name,
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

func (c *contextImpl) WithTimeout(timeout time.Duration) (Context, context.CancelFunc) {
	// Create new timeout context
	timeoutCtx, cancel := context.WithTimeout(c.ctx, timeout)

	// Create new context impl with timeout
	newCtx := &contextImpl{
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
	c.errorCause = err
	c.errorStack = append(c.errorStack, fmt.Sprintf("%v", err))
	if stackTrace := debug.Stack(); len(stackTrace) > 0 {
		c.errorStack = append(c.errorStack, string(stackTrace))
	}
	c.Response.SetStatusCode(code)
	c.Abort()

	// Trigger cancellation
	select {
	case <-c.done:
	default:
		close(c.done)
	}
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

// Update Get method to use sync.Map methods
func (c *contextImpl) Get(key string) (interface{}, bool) {
	return c.store.Load(key)
}

func (c *contextImpl) Set(key string, value interface{}) {
	c.store.Store(key, value)
}

func (c *contextImpl) Redirect(code int, url string) error {
	c.Response.Header.Set("Location", url)
	c.Response.SetStatusCode(code)
	return nil
}

func (c *contextImpl) GetTraceID() string {
	return c.traceID
}

func (c *contextImpl) GetSpans() []*tracingSpan {
	return c.spans
}

func (c *contextImpl) EndSpan(name string) {
	for _, span := range c.spans {
		if span.name == name && span.endTime.IsZero() {
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
