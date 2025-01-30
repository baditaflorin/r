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
}

type PanicHandlerFunc func(Context, interface{})

// RouterImpl implements the Router interface using fasthttp-routing
type RouterImpl struct {
	router                  *routing.Router
	group                   *routing.RouteGroup
	upgrader                websocket.FastHTTPUpgrader
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
		router: routing.New(),
		upgrader: websocket.FastHTTPUpgrader{
			EnableCompression: true,
			CheckOrigin: func(ctx *fasthttp.RequestCtx) bool {
				return true
			},
		},
		routes:       make(map[string]*Route),
		routeMetrics: NewDefaultMetricsCollector(), // Now this will work
	}
	r.group = r.router.Group("")
	r.middlewareProvider = NewMiddlewareProvider(r)
	return r
}

// Implement Router methods
func (r *RouterImpl) GET(path string, handlers ...HandlerFunc) Router {
	r.addRouteMetadata("GET", path, handlers...)
	r.group.Get(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *RouterImpl) POST(path string, handlers ...HandlerFunc) Router {
	r.group.Post(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *RouterImpl) DELETE(path string, handlers ...HandlerFunc) Router {
	r.group.Delete(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *RouterImpl) PUT(path string, handlers ...HandlerFunc) Router {
	r.group.Put(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *RouterImpl) PATCH(path string, handlers ...HandlerFunc) Router {
	r.group.Patch(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *RouterImpl) HEAD(path string, handlers ...HandlerFunc) Router {
	r.group.Head(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *RouterImpl) OPTIONS(path string, handlers ...HandlerFunc) Router {
	r.group.Options(path, r.wrapHandlers(handlers...)...)
	return r
}

func (r *RouterImpl) Static(prefix, root string) Router {
	r.group.Get(prefix+"/*", staticHandler(root))
	return r
}

func (r *RouterImpl) FileServer(path, root string) Router {
	r.group.Get(path, staticHandler(root))
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
		h := h
		wrapped[i] = func(c *routing.Context) error {
			start := time.Now()
			ctx := &contextImpl{
				Context:    c,
				router:     r,
				handlers:   append(make([]HandlerFunc, 0), h),
				handlerIdx: -1,
			}

			var err error
			defer func() {
				duration := time.Since(start)
				r.updateRouteMetrics(
					string(c.Method()),
					string(c.Path()),
					duration,
					err,
				)
			}()

			ctx.Next()
			err = ctx.Error()
			return err
		}
	}
	return wrapped
}

func staticHandler(root string) routing.Handler {
	fs := &fasthttp.FS{
		Root:            root,
		IndexNames:      []string{"index.html"},
		Compress:        true,
		CompressBrotli:  true,
		AcceptByteRange: true,
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
