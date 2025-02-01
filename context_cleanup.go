package r

import "time"

func (c *ContextImpl) Cleanup() {
	c.cancelContext()
	c.endRootSpan()
	c.endOpenSpans()
	c.recordFinalMetrics()
	c.closeDoneChannel()
	c.resetAndPool()
}

func (c *ContextImpl) cancelContext() {
	if c.cancel != nil {
		c.cancel()
	}
}

func (c *ContextImpl) endRootSpan() {
	if c.rootSpan != nil {
		c.rootSpan.endTime = time.Now()
		if c.metrics != nil {
			c.metrics.RecordTiming("request.total_time",
				c.rootSpan.endTime.Sub(c.rootSpan.startTime),
				map[string]string{
					"path":       c.Path(),
					"method":     c.Method(),
					"request_id": c.requestID,
					"span":       "root",
				})
		}
	}
}

func (c *ContextImpl) endOpenSpans() {
	now := time.Now()
	for _, span := range c.spans {
		if span.endTime.IsZero() {
			span.endTime = now
		}
	}
}

func (c *ContextImpl) recordFinalMetrics() {
	if c.metrics != nil {
		c.metrics.RecordTiming("request.total_time",
			time.Since(c.startTime),
			map[string]string{
				"path":       c.Path(),
				"method":     c.Method(),
				"request_id": c.requestID,
			})

		if err := c.Error(); err != nil {
			c.metrics.IncrementCounter("request.errors",
				map[string]string{
					"path":   c.Path(),
					"method": c.Method(),
					"error":  err.Error(),
				})
		}
	}
}

func (c *ContextImpl) closeDoneChannel() {
	select {
	case <-c.done:
		// already closed; do nothing
	default:
		close(c.done)
	}
}

func (c *ContextImpl) resetAndPool() {
	c.Reset()
	contextPool.Put(c)
}
