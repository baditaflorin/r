package main

import (
	"context"
	"fmt"
	"github.com/baditaflorin/l"
	"github.com/baditaflorin/r"
	"os"
)

// ChatHandler handles WebSocket chat functionality
type ChatHandler struct {
	logger l.Logger
}

func NewChatHandler(logger l.Logger) *ChatHandler {
	return &ChatHandler{
		logger: logger.With("component", "chat_handler"),
	}
}

func (h *ChatHandler) OnConnect(conn r.WSConnection) {
	h.logger.Info("Client connected",
		"client_id", conn.ID(),
		"remote_addr", conn.RemoteAddr(),
	)
}

func (h *ChatHandler) OnMessage(conn r.WSConnection, msg []byte) {
	h.logger.Debug("Received message",
		"client_id", conn.ID(),
		"message_size", len(msg),
	)
	// Echo the message back
	if err := conn.WriteMessage(1, msg); err != nil {
		h.logger.Error("Failed to write message",
			"error", err,
			"client_id", conn.ID(),
		)
	}
}

func (h *ChatHandler) OnClose(conn r.WSConnection) {
	h.logger.Info("Client disconnected",
		"client_id", conn.ID(),
	)
}

func main() {
	// Configure structured logger
	config := l.Config{
		Output:      os.Stdout,
		FilePath:    "logs/app.log",
		JsonFormat:  true,
		AsyncWrite:  true,
		BufferSize:  1024 * 1024,      // 1MB buffer
		MaxFileSize: 10 * 1024 * 1024, // 10MB max file size
		MaxBackups:  5,
		AddSource:   true,
		Metrics:     true,
	}

	// Create logger factory and logger
	factory := l.NewStandardFactory()
	logger, err := factory.CreateLogger(config)
	if err != nil {
		panic(err)
	}
	defer logger.Close()

	// Create base logger with service context
	baseLogger := logger.With(
		"service", "websocket_server",
		"environment", os.Getenv("ENV"),
	)

	// Create metrics collector
	metrics, err := factory.CreateMetricsCollector(config)
	if err != nil {
		baseLogger.Error("Failed to create metrics collector", "error", err)
		panic(err)
	}

	// Create health checker
	healthChecker, err := factory.CreateHealthChecker(logger)
	if err != nil {
		baseLogger.Error("Failed to create health checker", "error", err)
		panic(err)
	}
	healthChecker.Start(context.Background())
	defer healthChecker.Stop()

	// Create a new router
	router := r.NewRouter()

	// Add custom logging middleware
	router.Use(func(ctx r.Context) {
		reqLogger := baseLogger.With(
			"request_id", ctx.RequestID(),
			"method", ctx.Method(),
			"path", ctx.Path(),
		)
		ctx.Set("logger", reqLogger)
		ctx.Next()
	})

	// Add HTTP routes
	router.GET("/ping", func(ctx r.Context) {
		reqLogger := baseLogger.With(
			"request_id", ctx.RequestID(),
			"method", ctx.Method(),
			"path", ctx.Path(),
		)
		reqLogger.Info("Handling ping request")
		ctx.String(200, "pong")
	})

	// Add error test endpoint
	router.GET("/error", func(ctx r.Context) {
		reqLogger := baseLogger.With(
			"request_id", ctx.RequestID(),
			"method", ctx.Method(),
			"path", ctx.Path(),
		)
		reqLogger.Info("Handling error test request")
		ctx.AbortWithError(400, fmt.Errorf("test error"))
	})

	// Add JSON endpoint
	router.POST("/api/data", func(ctx r.Context) {
		reqLogger := baseLogger.With(
			"request_id", ctx.RequestID(),
			"method", ctx.Method(),
			"path", ctx.Path(),
		)
		reqLogger.Info("Handling data request")
		ctx.JSON(200, map[string]interface{}{
			"status": "success",
			"data":   "Hello, World!",
		})
	})

	// Add WebSocket endpoint
	router.WS("/ws/chat", NewChatHandler(baseLogger))

	// Create API group
	api := router.Group("/api/v1")
	api.GET("/status", func(ctx r.Context) {
		reqLogger := baseLogger.With(
			"request_id", ctx.RequestID(),
			"method", ctx.Method(),
			"path", ctx.Path(),
		)

		// Check health
		if err := healthChecker.Check(); err != nil {
			reqLogger.Error("Health check failed", "error", err)
			ctx.JSON(500, map[string]interface{}{
				"status": "unhealthy",
				"error":  err.Error(),
			})
			return
		}

		// Get metrics
		stats := metrics.GetMetrics()

		reqLogger.Info("Handling status request",
			"total_messages", stats.TotalMessages,
			"error_messages", stats.ErrorMessages,
		)

		ctx.JSON(200, map[string]interface{}{
			"status":  "operational",
			"metrics": stats,
		})
	})

	// Create and start the server
	serverConfig := r.DefaultConfig()

	// Set the router in the config
	serverConfig.Handler = router

	// Create and start the server with the config
	server := r.NewServer(serverConfig)
	baseLogger.Info("Starting server", "port", ":8080")
	if err := server.Start(":8080"); err != nil {
		baseLogger.Error("Server failed", "error", err)
		panic(err)
	}
}
