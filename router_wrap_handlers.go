// wrap_handlers.go
package r

import (
	"github.com/qiangxue/fasthttp-routing"
)

// buildHandlerChain combines the router's middleware with a specific handler,
// returning the complete chain of HandlerFuncs.
func buildHandlerChain(router *RouterImpl, handler HandlerFunc) []HandlerFunc {
	chain := make([]HandlerFunc, 0, len(router.middleware)+1)
	chain = append(chain, router.middleware...)
	chain = append(chain, handler)
	return chain
}

// executeHandlerChain initializes the handler index and executes the handler chain.
func executeHandlerChain(ctx *ContextImpl) {
	ctx.handlerIdx = -1
	ctx.Next()
}

// syncStatusCode copies the response status code from the enhanced context (ctx)
// to the underlying routing context (c).
func syncStatusCode(ctx *ContextImpl, c *routing.Context) {
	statusCode := ctx.RequestCtx().Response.StatusCode()
	if statusCode > 0 {
		c.SetStatusCode(statusCode)
	}
}

// createWrappedHandler returns a routing.Handler that wraps a single HandlerFunc
// by building the middleware chain, executing it, and syncing the response status.
func createWrappedHandler(router *RouterImpl, handler HandlerFunc) routing.Handler {
	return func(c *routing.Context) error {
		// Create an enhanced context from the routing context.
		ctx := newContextImpl(c)
		// Build the full chain: router middleware plus the specific handler.
		ctx.handlers = buildHandlerChain(router, handler)
		// Execute the handler chain.
		executeHandlerChain(ctx)
		// Sync the status code back to the original routing context.
		syncStatusCode(ctx, c)
		// Return any error that occurred during chain execution.
		return ctx.Error()
	}
}

// wrapHandlers wraps the provided HandlerFunc slice using the router's middleware chain,
// returning a slice of routing.Handler functions ready to be registered.
func wrapHandlers(router *RouterImpl, handlers ...HandlerFunc) []routing.Handler {
	wrapped := make([]routing.Handler, len(handlers))
	for i, h := range handlers {
		wrapped[i] = createWrappedHandler(router, h)
	}
	return wrapped
}
