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
