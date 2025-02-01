// File: ws_config.go
package r

import "time"

// wsConfig contains configuration parameters used by the pumps.
type wsConfig struct {
	ReadLimit    int64         // maximum allowed message size in bytes
	ReadTimeout  time.Duration // read deadline duration
	WriteTimeout time.Duration // write deadline duration
	PingInterval time.Duration // interval for sending ping messages
	PongWait     time.Duration // duration to wait after a pong
}

// defaultWSConfig returns a default configuration.
func defaultWSConfig() wsConfig {
	return wsConfig{
		ReadLimit:    10 * 1024 * 1024, // 10 MB
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		PingInterval: 54 * time.Second,
		PongWait:     5 * time.Second,
	}
}

// getWSConfig returns the configuration for a connection.
// (You could later store a wsConfig on your wsConnection if you want per-connection settings.)
func (c *wsConnection) getWSConfig() wsConfig {
	// For now, we return the default configuration.
	// Later, you might allow setting a custom configuration on the connection.
	return defaultWSConfig()
}
