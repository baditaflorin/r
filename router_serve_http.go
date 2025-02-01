package r

import (
	"encoding/json"
	routing "github.com/qiangxue/fasthttp-routing"
	"github.com/valyala/fasthttp"
	"time"
)

func (r *RouterImpl) ServeHTTP(ctx *fasthttp.RequestCtx) {
	// The code uses ctx directly, so no need to create a new variable.

	// Initialize metrics tags and defer cleanup.
	tags := r.initializeTags(ctx)
	defer r.cleanupTags(tags)

	start := time.Now()
	// Defer panic handling and timing metric recording.
	defer r.handlePanicAndMetrics(ctx, start, tags)

	// Process the request using the underlying router.
	r.router.HandleRequest(ctx)

	// Provide default 404 response if needed.
	r.handleNotFound(ctx)

	// Record error metrics for responses with status >= 400.
	r.handleErrorMetrics(ctx, tags)
}

// initializeTags builds a map of metrics tags from the request.
func (r *RouterImpl) initializeTags(ctx *fasthttp.RequestCtx) map[string]string {
	tags := tagsPool.Get().(map[string]string)
	tags["method"] = string(ctx.Method())
	tags["path"] = string(ctx.Path())
	return tags
}

// cleanupTags clears and returns the tags map to the pool.
func (r *RouterImpl) cleanupTags(tags map[string]string) {
	for k := range tags {
		delete(tags, k)
	}
	tagsPool.Put(tags)
}

// handlePanicAndMetrics recovers from panics, invokes the panic handler if set,
// increments panic metrics, and records the request duration.
func (r *RouterImpl) handlePanicAndMetrics(ctx *fasthttp.RequestCtx, start time.Time, tags map[string]string) {
	if rcv := recover(); rcv != nil {
		if r.panicHandler != nil {
			impl := newContextImpl(&routing.Context{RequestCtx: ctx})
			r.panicHandler(impl, rcv)
		} else {
			ctx.Error("Internal Server Error", fasthttp.StatusInternalServerError)
		}

		if r.routeMetrics != nil {
			r.routeMetrics.IncrementCounter("router.panic", map[string]string{
				"method": string(ctx.Method()),
				"path":   string(ctx.Path()),
			})
		}
	}
	if r.routeMetrics != nil {
		duration := time.Since(start)
		r.routeMetrics.RecordTiming("router.request.duration", duration, tags)
	}
}

// handleNotFound sets a JSON error response if no route was matched.
func (r *RouterImpl) handleNotFound(ctx *fasthttp.RequestCtx) {
	if ctx.Response.StatusCode() == fasthttp.StatusNotFound && r.notFoundHandler == nil {
		ctx.Response.Header.SetContentType("application/json")
		jsonBody, err := json.Marshal(map[string]string{
			"error": "route not found",
			"path":  string(ctx.Request.URI().Path()),
		})
		if err != nil {
			ctx.Error("Not Found", fasthttp.StatusNotFound)
		} else {
			ctx.Response.SetBody(jsonBody)
		}
	}
}

// handleErrorMetrics increments error counters if the response status code indicates an error.
func (r *RouterImpl) handleErrorMetrics(ctx *fasthttp.RequestCtx, tags map[string]string) {
	if ctx.Response.StatusCode() >= 400 && r.routeMetrics != nil {
		r.routeMetrics.IncrementCounter("router.error", tags)
	}
}
