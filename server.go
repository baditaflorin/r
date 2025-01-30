package r

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/valyala/fasthttp"
)

// Server defines the interface for the HTTP server
type Server interface {
	Start(address string) error
	Stop() error
	WithConfig(config Config) Server
	WithRouter(router Router) Server
	WithMiddleware(middleware ...MiddlewareFunc) Server
}

type serverImpl struct {
	server   *fasthttp.Server
	router   *RouterImpl
	config   Config
	shutdown chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
}

func NewServer(config Config) *serverImpl {
	if config.Handler == nil {
		config.Handler = NewRouter()
	}

	s := &serverImpl{
		router:   config.Handler.(*RouterImpl),
		config:   config,
		shutdown: make(chan struct{}),
	}

	// Set up default panic handler
	s.router.PanicHandler(func(c Context, rcv interface{}) {
		err := fmt.Errorf("panic: %v\n%s", rcv, debug.Stack())
		c.AbortWithError(http.StatusInternalServerError, err)
	})

	return s
}

func (s *serverImpl) buildHandler() fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				err := fmt.Errorf("panic recovered: %v\nStack: %s", r, stack)

				// Log the error
				if logger, ok := ctx.UserValue("logger").(Logger); ok {
					logger.Error("Panic recovered in request handler",
						"error", err,
						"path", string(ctx.Path()),
						"method", string(ctx.Method()),
						"request_id", ctx.Response.Header.Peek("X-Request-ID"),
					)
				}

				// Return 500 error to client
				ctx.Error("Internal Server Error", fasthttp.StatusInternalServerError)
			}
		}()

		c := newRoutingContext(ctx)
		reqCtx := newContextImpl(c)

		// Ensure request ID
		if reqCtx.RequestID() == "" {
			reqCtx.requestID = uuid.New().String()
		}
		ctx.Response.Header.Set("X-Request-ID", reqCtx.requestID)

		// Set reasonable timeout for the entire request
		timeoutCtx, cancel := context.WithTimeout(context.Background(), s.config.ReadTimeout)
		defer cancel()

		done := make(chan struct{})
		go func() {
			s.router.router.HandleRequest(ctx)
			close(done)
		}()

		select {
		case <-timeoutCtx.Done():
			ctx.Error("Request Timeout", fasthttp.StatusGatewayTimeout)
			return
		case <-done:
			return
		}
	}
}

func (s *serverImpl) Start(address string) error {
	s.server = &fasthttp.Server{
		Handler:            s.buildHandler(),
		ReadTimeout:        s.config.ReadTimeout,
		WriteTimeout:       s.config.WriteTimeout,
		IdleTimeout:        s.config.IdleTimeout,
		MaxRequestBodySize: s.config.MaxRequestBodySize,
	}

	if s.config.CertFile != "" && s.config.KeyFile != "" {
		return s.server.ListenAndServeTLS(address, s.config.CertFile, s.config.KeyFile)
	}
	return s.server.ListenAndServe(address)
}

func (s *serverImpl) Stop() error {
	s.mu.Lock()
	close(s.shutdown)
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return s.server.ShutdownWithContext(ctx)
}
