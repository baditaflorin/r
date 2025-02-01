package main

import (
	"github.com/baditaflorin/l"
	"github.com/baditaflorin/r"
	"os"
	"time"
)

func main() {
	// Configure logger
	logger, err := l.NewStandardFactory().CreateLogger(l.Config{
		Output:     os.Stdout,
		JsonFormat: true,
	})
	if err != nil {
		panic(err)
	}
	defer logger.Close()

	// Create router
	router := r.NewRouter()

	// Create middleware provider
	middlewareProvider := r.NewMiddlewareProvider(router.(*r.RouterImpl))

	// Add middleware
	router.Use(
		middlewareProvider.RequestID(),
		middlewareProvider.Logger("%s | %3d | %13v | %15s | %s"),
		middlewareProvider.Security(),
		middlewareProvider.RateLimit(100, time.Minute),
		middlewareProvider.CORS([]string{"*"}),
	)

	// Add routes
	router.GET("/hello", func(ctx r.Context) {
		ctx.JSON(200, map[string]interface{}{
			"message":    "Hello World!",
			"request_id": ctx.RequestID(),
		})
	})

	// Add API group with stricter rate limit
	api := router.Group("/api")
	api.Use(
		middlewareProvider.RateLimit(10, time.Minute),
	)

	api.GET("/protected", func(ctx r.Context) {
		ctx.JSON(200, map[string]interface{}{
			"message":    "Protected endpoint!",
			"request_id": ctx.RequestID(),
		})
	})

	// Add WebSocket handler
	chatHandler := &ChatHandler{logger: logger}
	router.WS("/ws/chat", chatHandler)

	// Create and start server
	server := r.NewServer(r.Config{
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	})

	logger.Info("Starting server on :8080")
	if err := server.Start(":8080"); err != nil {
		logger.Error("Server failed", "error", err)
		panic(err)
	}
}

type ChatHandler struct {
	logger l.Logger
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
