package r

import (
	"fmt"
	"github.com/fasthttp/websocket"
	"github.com/qiangxue/fasthttp-routing"
	"github.com/valyala/fasthttp"
	"sync"
	"sync/atomic"
	"time"
)

// Router defines the interface for HTTP routing
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
	ServeHTTP(ctx *fasthttp.RequestCtx)
}

type PanicHandlerFunc func(Context, interface{})

// RouterImpl implements the Router interface using fasthttp-routing
type RouterImpl struct {
	router     *routing.Router
	group      *routing.RouteGroup
	upgrader   websocket.FastHTTPUpgrader
	middleware []HandlerFunc // Add this field to store middleware
	logger     Logger

	panicHandler            PanicHandlerFunc
	methodNotAllowedHandler HandlerFunc
	middlewareProvider      MiddlewareProvider

	// Add new fields for route management
	routes       map[string]*Route
	routesMu     sync.RWMutex
	validator    RouteValidator
	routeMetrics MetricsCollector
}

type Route struct {
	Path        string
	Method      string
	Handler     string
	Description string
	Params      []RouteParam
	Added       time.Time
	Tags        []string

	// Add fields for monitoring
	totalCalls atomic.Uint64
	errorCount atomic.Uint64
	lastCalled atomic.Int64
	avgLatency atomic.Int64
}

type RouteParam struct {
	Name        string
	Type        string
	Required    bool
	Description string
	Validation  string // regex or validation rules
}

type RouteValidator interface {
	ValidateRoute(path, method string) error
	ValidateParams(params []RouteParam) error
}

// NewRouter creates a new Router instance
func NewRouter() Router {
	r := &RouterImpl{
		router:     routing.New(),
		middleware: make([]HandlerFunc, 0),
		routes:     make(map[string]*Route),
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

// Implement Router methods
func (r *RouterImpl) GET(path string, handlers ...HandlerFunc) Router {
	r.addRouteMetadata("GET", path, handlers...)
	r.group.Get(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *RouterImpl) POST(path string, handlers ...HandlerFunc) Router {
	r.addRouteMetadata("POST", path, handlers...)
	r.group.Post(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *RouterImpl) DELETE(path string, handlers ...HandlerFunc) Router {
	r.addRouteMetadata("DELETE", path, handlers...)
	r.group.Delete(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *RouterImpl) PUT(path string, handlers ...HandlerFunc) Router {
	r.addRouteMetadata("PUT", path, handlers...)
	r.group.Put(path, r.wrapHandlers(handlers...)...)
	return r
}
func (r *RouterImpl) PATCH(path string, handlers ...HandlerFunc) Router {
	r.addRouteMetadata("PATCH", path, handlers...)
	r.group.Patch(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *RouterImpl) HEAD(path string, handlers ...HandlerFunc) Router {
	r.addRouteMetadata("HEAD", path, handlers...)
	r.group.Head(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *RouterImpl) OPTIONS(path string, handlers ...HandlerFunc) Router {
	r.addRouteMetadata("OPTIONS", path, handlers...)
	r.group.Options(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *RouterImpl) Static(prefix, root string) Router {
	// You might want to record that this is a static endpoint.
	r.addRouteMetadata("GET", prefix+"/*", nil) // or include handlers if appropriate
	r.group.Get(prefix+"/*", staticHandler(prefix, root))
	return r
}

func (r *RouterImpl) FileServer(path, root string) Router {
	r.addRouteMetadata("GET", path, nil)
	r.group.Get(path, staticHandler(path, root))
	return r
}

func (r *RouterImpl) NotFound(handler HandlerFunc) {
	r.router.NotFound(r.wrapHandlers(handler)[0])
}

func (r *RouterImpl) MethodNotAllowed(handler HandlerFunc) {
	r.methodNotAllowedHandler = handler
	r.router.NotFound(func(c *routing.Context) error {
		ctx := newContextImpl(c)
		if r.methodNotAllowedHandler != nil {
			r.methodNotAllowedHandler(ctx)
		}
		return nil
	})
}

func (r *RouterImpl) PanicHandler(handler PanicHandlerFunc) {
	r.panicHandler = handler
}

func (r *RouterImpl) wrapHandlers(handlers ...HandlerFunc) []routing.Handler {
	wrapped := make([]routing.Handler, len(handlers))

	for i, h := range handlers {
		handler := h
		wrapped[i] = func(c *routing.Context) error {
			ctx := newContextImpl(c)

			allHandlers := make([]HandlerFunc, 0, len(r.middleware)+1)
			allHandlers = append(allHandlers, r.middleware...)
			allHandlers = append(allHandlers, handler)

			ctx.handlers = allHandlers
			ctx.handlerIdx = -1

			// Execute chain
			ctx.Next()

			// Get status code
			statusCode := ctx.RequestCtx().Response.StatusCode()
			if statusCode > 0 {
				// Ensure routing context has same status
				c.SetStatusCode(statusCode)
			}

			// Return error if we have one
			return ctx.Error()
		}
	}
	return wrapped
}

func staticHandler(prefix, root string) routing.Handler {
	fs := &fasthttp.FS{
		Root:            root,
		IndexNames:      []string{"index.html"},
		Compress:        true,
		CompressBrotli:  true,
		AcceptByteRange: true,
		// Strip the prefix from the requested path.
		PathRewrite: func(ctx *fasthttp.RequestCtx) []byte {
			p := ctx.Request.URI().Path()
			// Only rewrite if the path starts with the prefix.
			if len(p) >= len(prefix) && string(p[:len(prefix)]) == prefix {
				return p[len(prefix):]
			}
			return p
		},
	}
	handler := fs.NewRequestHandler()
	return func(c *routing.Context) error {
		handler(c.RequestCtx)
		return nil
	}
}

func (r *RouterImpl) addRouteMetadata(method, path string, handlers ...HandlerFunc) {
	r.initializeIfNeeded()
	r.routesMu.Lock()
	defer r.routesMu.Unlock()

	route := &Route{
		Path:   path,
		Method: method,
		Added:  time.Now(),
	}

	// Validate route if validator is configured
	if r.validator != nil {
		if err := r.validator.ValidateRoute(path, method); err != nil {
			r.panicHandler(nil, fmt.Sprintf("invalid route %s %s: %v", method, path, err))
			return
		}
	}

	// Add metrics collection
	if r.routeMetrics != nil {
		r.routeMetrics.IncrementCounter("route.registered", map[string]string{
			"method": method,
			"path":   path,
		})
	}

	routeKey := method + path
	r.routes[routeKey] = route
}

// Add helper methods for route introspection
func (r *RouterImpl) GetRoutes() []*Route {
	r.routesMu.RLock()
	defer r.routesMu.RUnlock()

	routes := make([]*Route, 0, len(r.routes))
	for _, route := range r.routes {
		routes = append(routes, route)
	}
	return routes
}

// Add method to update route metrics
func (r *RouterImpl) updateRouteMetrics(method, path string, duration time.Duration, err error) {
	if r.routeMetrics == nil {
		return // Skip metrics collection if not configured
	}

	routeKey := method + path
	r.routesMu.RLock()
	route, exists := r.routes[routeKey]
	r.routesMu.RUnlock()

	if !exists {
		return
	}

	route.totalCalls.Add(1)
	route.lastCalled.Store(time.Now().UnixNano())

	if err != nil {
		route.errorCount.Add(1)
	}

	// Update average latency using exponential moving average
	currentAvg := float64(route.avgLatency.Load())
	newAvg := (currentAvg*0.9 + float64(duration.Nanoseconds())*0.1)
	route.avgLatency.Store(int64(newAvg))
}

func (r *RouterImpl) initializeIfNeeded() {
	r.routesMu.Lock()
	defer r.routesMu.Unlock()

	if r.routes == nil {
		r.routes = make(map[string]*Route)
	}

	if r.routeMetrics == nil && r.router != nil {
		// Initialize with default metrics collector if available
		r.routeMetrics = NewDefaultMetricsCollector()
	}
}

// Add this method to the RouterImpl struct in router.go

func (r *RouterImpl) ServeHTTP(ctx *fasthttp.RequestCtx) {
	// Create a new routing context
	c := &routing.Context{RequestCtx: ctx}

	// Create metrics tags
	tags := tagsPool.Get().(map[string]string)
	tags["method"] = string(ctx.Method())
	tags["path"] = string(ctx.Path())
	defer func() {
		// Clean up the map before returning it to the pool
		for k := range tags {
			delete(tags, k)
		}
		tagsPool.Put(tags)
	}()

	// Record request start time
	start := time.Now()

	// Handle panics
	defer func() {
		if rcv := recover(); rcv != nil {
			if r.panicHandler != nil {
				impl := newContextImpl(c)
				r.panicHandler(impl, rcv)
			} else {
				ctx.Error("Internal Server Error", fasthttp.StatusInternalServerError)
			}

			if r.routeMetrics != nil {
				r.routeMetrics.IncrementCounter("router.panic",
					map[string]string{
						"method": string(ctx.Method()),
						"path":   string(ctx.Path()),
					})
			}
		}

		// Record request duration
		if r.routeMetrics != nil {
			duration := time.Since(start)
			r.routeMetrics.RecordTiming("router.request.duration",
				duration,
				tags)
		}
	}()

	// Handle the request using the router
	r.router.HandleRequest(ctx)

	// Check for any error status codes
	if c.Response.StatusCode() >= 400 {
		if r.routeMetrics != nil {
			r.routeMetrics.IncrementCounter("router.error",
				tags)
		}
	}
}
