package r

import (
	"context"
	"github.com/google/uuid"
	routing "github.com/qiangxue/fasthttp-routing"
	"sync"
	"time"
)

func newContextImpl(c *routing.Context) *ContextImpl {
	// Create a timeout context.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	// Obtain an instance from the pool.
	impl := getContextImplFromPool()

	// Initialize the context with required fields.
	impl.initialize(c, ctx, cancel)

	// Start the root span.
	impl.startRootSpan()

	// Launch a goroutine that waits for the timeout and then cleans up.
	go impl.monitorContext(ctx)

	return impl
}

// getContextImplFromPool retrieves a ContextImpl from the pool.
func getContextImplFromPool() *ContextImpl {
	return contextPool.Get().(*ContextImpl)
}

// initialize sets up the ContextImpl fields.
func (c *ContextImpl) initialize(routingCtx *routing.Context, ctx context.Context, cancel context.CancelFunc) {
	c.Context = routingCtx
	c.ctx = ctx
	c.cancel = cancel
	c.requestID = uuid.New().String()
	c.store = &sync.Map{}
	c.handlers = nil
	c.startTime = time.Now()
	c.spans = nil
	c.timeouts = make(map[string]time.Duration)
	c.done = make(chan struct{})
	c.errorStack = nil
	c.traceID = uuid.New().String()
}

// startRootSpan creates and assigns the root span.
func (c *ContextImpl) startRootSpan() {
	c.rootSpan = c.startSpan("request", map[string]string{
		"request_id": c.requestID,
		"method":     c.Method(),
		"path":       c.Path(),
		"remote_ip":  c.RealIP(),
		"trace_id":   c.traceID,
	})
}

// monitorContext waits for the context's done signal, then cleans up and returns the instance to the pool.
func (c *ContextImpl) monitorContext(ctx context.Context) {
	<-ctx.Done()
	c.Cleanup()
	contextPool.Put(c)
}
