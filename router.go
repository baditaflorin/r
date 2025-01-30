package r

import (
	"github.com/fasthttp/websocket"
	"github.com/qiangxue/fasthttp-routing"
	"github.com/valyala/fasthttp"
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
	}
	r.group = r.router.Group("")
	r.middlewareProvider = NewMiddlewareProvider(r)
	return r
}

// Implement Router methods
func (r *RouterImpl) GET(path string, handlers ...HandlerFunc) Router {
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
			ctx := &contextImpl{
				Context:    c,
				router:     r,
				handlers:   append(make([]HandlerFunc, 0), h),
				handlerIdx: -1,
			}
			ctx.Next()
			return nil
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
