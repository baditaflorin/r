package r

import (
	"context"
	"fmt"
	"github.com/fasthttp/websocket"
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
	// Signal we're starting shutdown
	s.logger.Info("Starting graceful shutdown")

	// Create context with server's shutdown timeout
	ctx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()

	// Stop accepting new connections
	s.server.DisableKeepalive = true

	// Track cleanup tasks
	var wg sync.WaitGroup
	errCh := make(chan error, 4)

	// 1. Shutdown HTTP server with graceful connection draining
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.logger.Info("Stopping HTTP server")

		if err := s.server.ShutdownWithContext(ctx); err != nil {
			errCh <- fmt.Errorf("HTTP server shutdown error: %w", err)
			return
		}

		s.logger.Info("HTTP server stopped successfully")
	}()

	// 2. Drain WebSocket connections
	wg.Add(1)
	go func() {
		defer wg.Done()
		deadline := time.Now().Add(s.shutdownTimeout / 2)

		s.activeConns.Range(func(key, value interface{}) bool {
			if conn, ok := value.(WSConnection); ok {
				// Send close message to clients
				closeMsg := websocket.FormatCloseMessage(
					websocket.CloseServiceRestart,
					"Server is shutting down",
				)

				if err := conn.WriteMessage(websocket.CloseMessage, closeMsg); err != nil {
					s.logger.Error("Failed to send close message",
						"error", err,
						"conn_id", conn.ID())
				}

				// Set deadline for graceful close
				conn.SetReadDeadline(deadline)
				conn.SetWriteDeadline(deadline)
			}
			return true
		})

		s.logger.Info("WebSocket connections drained")
	}()

	// 3. Cleanup resources
	wg.Add(1)
	go func() {
		defer wg.Done()

		// Close metrics collector
		if s.metricsCollector != nil {
			if err := s.metricsCollector.Close(); err != nil {
				errCh <- fmt.Errorf("metrics collector shutdown error: %w", err)
			}
		}

		// Flush logs
		if syncer, ok := s.logger.(interface{ Sync() error }); ok {
			if err := syncer.Sync(); err != nil {
				errCh <- fmt.Errorf("logger sync error: %w", err)
			}
		}

		s.logger.Info("Resources cleaned up")
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
		close(errCh)
		var errors []error
		for err := range errCh {
			errors = append(errors, err)
		}
		if len(errors) > 0 {
			return fmt.Errorf("shutdown completed with errors: %v", errors)
		}
		s.logger.Info("Server shutdown completed successfully")
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
