package r

import (
	"fmt"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
)

// Server defines the interface for the HTTP server
type Server interface {
	Start(address string) error
	Stop() error
	WithConfig(config Config) Server
	WithRouter(router Router) Server
	WithMiddleware(middleware ...MiddlewareFunc) Server
	ServeHTTP(ctx *fasthttp.RequestCtx)
}

type ServerImpl struct {
	server   *fasthttp.Server
	router   *RouterImpl
	config   Config
	shutdown chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex

	healthChecks     []HealthCheck
	activeConns      sync.Map
	shutdownTimeout  time.Duration
	metricsCollector MetricsCollector
	logger           Logger
}

type HealthCheck struct {
	Name     string
	Check    func() error
	Interval time.Duration
}

func NewServer(config Config) *ServerImpl {
	if config.Handler == nil {
		config.Handler = NewRouter()
	}

	// Create default logger if not provided
	logger := NewDefaultLogger()

	s := &ServerImpl{
		router:           config.Handler.(*RouterImpl),
		config:           config,
		shutdown:         make(chan struct{}),
		shutdownTimeout:  30 * time.Second, // Default shutdown timeout
		metricsCollector: NewDefaultMetricsCollector(),
		logger:           logger,
	}

	// Set up default panic handler
	s.router.PanicHandler(func(c Context, rcv interface{}) {
		err := fmt.Errorf("panic: %v\n%s", rcv, debug.Stack())
		s.logger.Error("Panic in request handler",
			"error", err,
			"stack", debug.Stack(),
		)
		c.AbortWithError(http.StatusInternalServerError, err)
	})

	return s
}

func (s *ServerImpl) Start(address string) error {
	s.server = &fasthttp.Server{
		Handler:            s.BuildHandler(),
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

func (s *ServerImpl) WithLogger(logger Logger) *ServerImpl {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger = logger
	return s
}

func (s *ServerImpl) WithMetricsCollector(collector MetricsCollector) *ServerImpl {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metricsCollector = collector
	return s
}

func (s *ServerImpl) ServeHTTP(ctx *fasthttp.RequestCtx) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("Panic in request handler",
				"error", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()),
				"path", string(ctx.Path()),
				"method", string(ctx.Method()),
			)
			ctx.Error("Internal Server Error", fasthttp.StatusInternalServerError)
		}
	}()

	// Create a new request handler
	handler := s.BuildHandler()

	// Handle the request
	handler(ctx)
}
