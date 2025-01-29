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

	// Create middleware provider with the router
	middlewareProvider := r.NewMiddlewareProvider(router.(*r.RouterImpl))

	// Add middleware using the provider
	router.Use(
		// Request ID middleware from provider
		middlewareProvider.RequestID(),

		// Logger middleware from provider
		middlewareProvider.Logger("%s | %3d | %13v | %15s | %s"),

		// Security headers from provider
		middlewareProvider.Security(),

		// Rate limiting from provider: 100 requests per minute
		middlewareProvider.RateLimit(100, time.Minute),

		// CORS middleware from provider
		middlewareProvider.CORS([]string{"*"}),
	)

	// Add routes
	router.GET("/hello", func(ctx r.Context) {
		// The request ID should now be properly set by the middleware
		ctx.JSON(200, map[string]interface{}{
			"message":    "Hello World!",
			"request_id": ctx.RequestID(), // This should now work
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
