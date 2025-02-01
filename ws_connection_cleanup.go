// File: connection_cleanup.go
package r

import (
	"fmt"
	"time"
)

// periodicCleanup runs a cleanup loop that periodically removes stale connections.
func (cm *ConnectionManager) periodicCleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		// Clean up stale connections and obtain the count of removed connections.
		staleCount := cm.cleanupStaleConnections()
		// Update the metrics based on the number of cleaned connections.
		cm.updateCleanupMetrics(staleCount)
	}
}

// cleanupStaleConnections iterates over all connections and removes those that are stale.
// It returns the number of connections that were removed.
func (cm *ConnectionManager) cleanupStaleConnections() int {
	staleCount := 0

	cm.connections.Range(func(key, value interface{}) bool {
		conn := value.(*wsConnection)
		// Determine if the connection is stale based on the lastPing time.
		if time.Since(time.Unix(0, conn.lastPing.Load())) > 10*time.Minute {
			cm.logger.Warn("Removing stale connection",
				"conn_id", conn.ID(),
				"last_ping", time.Unix(0, conn.lastPing.Load()))
			// Close the connection and remove it from the manager.
			conn.Close()
			cm.Remove(conn)
			staleCount++
		}
		return true
	})

	return staleCount
}

// updateCleanupMetrics updates the metrics counter if any connections were removed.
func (cm *ConnectionManager) updateCleanupMetrics(staleCount int) {
	if cm.metrics != nil && staleCount > 0 {
		cm.metrics.IncrementCounter("ws.connections.cleaned",
			map[string]string{"count": fmt.Sprintf("%d", staleCount)})
	}
}
