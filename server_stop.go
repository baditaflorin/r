package r

import (
	"context"
	"fmt"
	"github.com/fasthttp/websocket"
	"sync"
	"time"
)

// shutdownHTTP gracefully shuts down the HTTP server.
func (s *ServerImpl) shutdownHTTP(ctx context.Context, wg *sync.WaitGroup, errCh chan error) {
	defer wg.Done()
	s.logger.Info("Stopping HTTP server")
	if err := s.server.ShutdownWithContext(ctx); err != nil {
		errCh <- fmt.Errorf("HTTP server shutdown error: %w", err)
		return
	}
	s.logger.Info("HTTP server stopped successfully")
}

// drainWebSockets iterates over active WebSocket connections, sending a close message.
func (s *ServerImpl) drainWebSockets(wg *sync.WaitGroup) {
	defer wg.Done()
	deadline := time.Now().Add(s.shutdownTimeout / 2)
	s.activeConns.Range(func(key, value interface{}) bool {
		if conn, ok := value.(WSConnection); ok {
			closeMsg := websocket.FormatCloseMessage(
				websocket.CloseServiceRestart,
				"Server is shutting down",
			)
			if err := conn.WriteMessage(websocket.CloseMessage, closeMsg); err != nil {
				s.logger.Error("Failed to send close message",
					"error", err,
					"conn_id", conn.ID())
			}
			conn.SetReadDeadline(deadline)
			conn.SetWriteDeadline(deadline)
		}
		return true
	})
	s.logger.Info("WebSocket connections drained")
}

// cleanupResources handles closing the metrics collector and flushing logs.
func (s *ServerImpl) cleanupResources(wg *sync.WaitGroup, errCh chan error) {
	defer wg.Done()
	if s.metricsCollector != nil {
		if err := s.metricsCollector.Close(); err != nil {
			errCh <- fmt.Errorf("metrics collector shutdown error: %w", err)
		}
	}
	if syncer, ok := s.logger.(interface{ Sync() error }); ok {
		if err := syncer.Sync(); err != nil {
			errCh <- fmt.Errorf("logger sync error: %w", err)
		}
	}
	s.logger.Info("Resources cleaned up")
}

// Stop refactored into smaller helper functions.
func (s *ServerImpl) Stop() error {
	s.logger.Info("Starting graceful shutdown")
	ctx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()
	s.server.DisableKeepalive = true

	var wg sync.WaitGroup
	errCh := make(chan error, 3)

	// Launch shutdown tasks concurrently.
	wg.Add(3)
	go s.shutdownHTTP(ctx, &wg, errCh)
	go s.drainWebSockets(&wg)
	go s.cleanupResources(&wg, errCh)

	// Wait for all tasks to complete or for the context to timeout.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return fmt.Errorf("shutdown timeout exceeded: %w", ctx.Err())
	case <-done:
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
