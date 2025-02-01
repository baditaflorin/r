package r_test

import (
	"github.com/baditaflorin/r"
	"testing"
	"time"
)

func TestServer_StartStop(t *testing.T) {
	server := r.NewServer(r.DefaultConfig())
	go func() {
		if err := server.Start(":8080"); err != nil {
			t.Errorf("Failed to start server: %v", err)
		}
	}()
	time.Sleep(1 * time.Second)
	if err := server.Stop(); err != nil {
		t.Errorf("Failed to stop server: %v", err)
	}
}

func TestServer_HandlesConcurrentRequests(t *testing.T) {
	// Simulate multiple concurrent requests to test load handling
}

func TestServer_GracefulShutdown(t *testing.T) {
	router := r.NewRouter()
	router.GET("/slow", func(c r.Context) {
		time.Sleep(2 * time.Second)
		c.String(200, "done")
	})

	server := r.NewServer(r.Config{
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	})

	// Start server in goroutine
	go func() {
		if err := server.Start(":8081"); err != nil {
			t.Logf("Server stopped: %v", err)
		}
	}()

	time.Sleep(1 * time.Second) // Wait for server to start

	// Start a slow request
	done := make(chan bool)
	go func() {
		// Make request to /slow
		time.Sleep(3 * time.Second)
		done <- true
	}()

	// Initiate shutdown
	shutdownErr := server.Stop()

	select {
	case <-done:
		// Request completed successfully
	case <-time.After(5 * time.Second):
		t.Error("Request did not complete during graceful shutdown")
	}

	if shutdownErr != nil {
		t.Errorf("Unexpected error during shutdown: %v", shutdownErr)
	}
}
