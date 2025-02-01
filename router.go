package r

import (
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
	router          *routing.Router
	group           *routing.RouteGroup
	upgrader        websocket.FastHTTPUpgrader
	middleware      []HandlerFunc // Add this field to store middleware
	logger          Logger
	notFoundHandler HandlerFunc

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
	r.notFoundHandler = handler
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
	return wrapHandlers(r, handlers...)
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

func (r *RouterImpl) updateRouteMetrics(method, path string, duration time.Duration, err error) {
	UpdateRouteMetrics(r, method, path, duration, err)
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
