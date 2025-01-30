package r

import (
	"context"
	"fmt"
	"net"
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

func NewServer(config Config) *serverImpl {
	if config.Handler == nil {
		config.Handler = NewRouter()
	}

	// Create default logger if not provided
	logger := NewDefaultLogger()

	s := &serverImpl{
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
	// Create context with server's shutdown timeout
	ctx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()

	// Signal shutdown to all components
	close(s.shutdown)

	// Create wait group for tracking all cleanup tasks
	var wg sync.WaitGroup

	// Track cleanup tasks
	errChan := make(chan error, 3) // Buffer for potential errors

	// Shutdown HTTP server
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := s.server.ShutdownWithContext(ctx); err != nil {
			errChan <- fmt.Errorf("HTTP server shutdown error: %w", err)
		}
	}()

	// Cleanup active connections
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Set deadline for existing connections
		deadline := time.Now().Add(30 * time.Second)
		s.activeConns.Range(func(key, value interface{}) bool {
			if conn, ok := value.(net.Conn); ok {
				conn.SetDeadline(deadline)
			}
			return true
		})
	}()

	// Cleanup resources and flush logs
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Close any resource pools
		if s.metricsCollector != nil {
			if err := s.metricsCollector.Close(); err != nil {
				errChan <- fmt.Errorf("metrics collector shutdown error: %w", err)
			}
		}

		// Flush logs - ensure logger supports Sync()
		if syncer, ok := s.logger.(interface{ Sync() error }); ok {
			if err := syncer.Sync(); err != nil {
				errChan <- fmt.Errorf("logger sync error: %w", err)
			}
		}
	}()

	// Wait for cleanup with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	// Wait for either completion or timeout
	select {
	case <-ctx.Done():
		return fmt.Errorf("shutdown timeout exceeded: %w", ctx.Err())
	case <-done:
		// Check for any errors
		close(errChan)
		var errors []error
		for err := range errChan {
			errors = append(errors, err)
		}
		if len(errors) > 0 {
			return fmt.Errorf("shutdown completed with errors: %v", errors)
		}
		return nil
	}
}

func (s *serverImpl) WithLogger(logger Logger) *serverImpl {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger = logger
	return s
}

func (s *serverImpl) WithMetricsCollector(collector MetricsCollector) *serverImpl {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metricsCollector = collector
	return s
}
