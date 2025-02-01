package r

import (
	"github.com/google/uuid"
	"github.com/valyala/fasthttp"
)

// prepareRequestContext initializes the enhanced request context for the incoming request.
// It wraps the original fasthttp.RequestCtx into a routing context, creates a new ContextImpl,
// ensures that a request ID is present, and sets it in the response header.
func (s *ServerImpl) prepareRequestContext(ctx *fasthttp.RequestCtx) *ContextImpl {
	// Create a routing context wrapping the original fasthttp context.
	routingCtx := newRoutingContext(ctx)
	// Create the enhanced context using the routing context.
	reqCtx := newContextImpl(routingCtx)

	// Ensure the request has an ID; if not, generate one.
	if reqCtx.requestID == "" {
		reqCtx.requestID = uuid.New().String()
	}

	// Add the request ID to the response header.
	ctx.Response.Header.Set("X-Request-ID", reqCtx.requestID)
	return reqCtx
}

// preserveResponseStatus ensures that if the response's status code has been set,
// it is re-applied to the fasthttp context.
func preserveResponseStatus(ctx *fasthttp.RequestCtx) {
	if statusCode := ctx.Response.StatusCode(); statusCode > 0 {
		ctx.SetStatusCode(statusCode)
	}
}

// BuildHandler creates the final fasthttp.RequestHandler that wraps the entire request lifecycle.
// It prepares the request context, delegates the request to the router, and then preserves the status code.
func (s *ServerImpl) BuildHandler() fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		// Step 1: Prepare the request context.
		s.prepareRequestContext(ctx)

		// Step 2: Handle the request via the router.
		s.router.ServeHTTP(ctx)

		// Step 3: Preserve the status code from the response.
		preserveResponseStatus(ctx)
	}
}
