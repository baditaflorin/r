package r

import (
	"fmt"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const tokenScale = 1_000_000 // Scale factor for fixed-point arithmetic

// tokenBucket stores the rate limiting state.
type tokenBucket struct {
	tokens     atomic.Int64 // tokens scaled by tokenScale
	lastRefill atomic.Int64 // UnixNano timestamp of last refill
	stats      struct {
		allowCount  atomic.Int64
		rejectCount atomic.Int64
		refillCount atomic.Int64
	}
}

// defaultRateLimiter implements both local and distributed rate limiting.
type defaultRateLimiter struct {
	// Local rate limiting maps.
	tokens  map[string]float64
	lastReq map[string]time.Time

	// Bucket-based rate limiter.
	buckets map[string]*tokenBucket

	maxRate float64       // maximum allowed requests per period.
	per     time.Duration // the duration of each period.

	mu          sync.RWMutex
	cleanupTick *time.Ticker

	// Distributed rate limiting fields.
	redisClient RedisClient
	keyPrefix   string

	// Logging & Metrics.
	logger    Logger
	metrics   MetricsCollector
	debugMode bool
}

// ==============================
// Local Rate Limiting Helpers
// ==============================

// Allow checks whether a request is allowed using a token bucket algorithm.
func (rl *defaultRateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	bucket := rl.getOrCreateBucket(key, now)
	rl.refillBucket(bucket, now)

	newTokens := float64(bucket.tokens.Load()) / tokenScale
	if newTokens >= 1.0 {
		// Consume one token.
		bucket.tokens.Add(-tokenScale)
		bucket.stats.allowCount.Add(1)
		if rl.metrics != nil {
			rl.metrics.IncrementCounter("rate_limiter.request_allowed",
				map[string]string{
					"key":              key,
					"tokens_remaining": fmt.Sprintf("%.2f", newTokens-1),
				})
		}
		return true
	}

	bucket.stats.rejectCount.Add(1)
	if rl.metrics != nil {
		rl.metrics.IncrementCounter("rate_limiter.request_rejected",
			map[string]string{
				"key":              key,
				"tokens_remaining": fmt.Sprintf("%.2f", newTokens),
			})
	}
	return false
}

// getOrCreateBucket retrieves an existing bucket for a key or creates one.
func (rl *defaultRateLimiter) getOrCreateBucket(key string, now time.Time) *tokenBucket {
	bucket, exists := rl.buckets[key]
	if !exists {
		bucket = &tokenBucket{}
		bucket.tokens.Store(int64(rl.maxRate * tokenScale))
		bucket.lastRefill.Store(now.UnixNano())
		rl.buckets[key] = bucket
		bucket.stats.allowCount.Add(1)
		if rl.metrics != nil {
			rl.metrics.IncrementCounter("rate_limiter.bucket_created",
				map[string]string{"key": key})
		}
	}
	return bucket
}

// refillBucket updates the bucket with new tokens based on elapsed time.
func (rl *defaultRateLimiter) refillBucket(bucket *tokenBucket, now time.Time) {
	lastRefillTime := time.Unix(0, bucket.lastRefill.Load())
	elapsed := now.Sub(lastRefillTime).Seconds()

	currentTokens := float64(bucket.tokens.Load()) / tokenScale
	tokensToAdd := elapsed * (rl.maxRate / rl.per.Seconds())
	newTokens := math.Min(rl.maxRate, currentTokens+tokensToAdd)

	bucket.tokens.Store(int64(newTokens * tokenScale))
	bucket.lastRefill.Store(now.UnixNano())
	bucket.stats.refillCount.Add(1)
}

// localAllow provides a simpler local rate limiting method.
func (rl *defaultRateLimiter) localAllow(key string) bool {
	now := time.Now()
	if _, exists := rl.tokens[key]; !exists {
		rl.tokens[key] = rl.maxRate
		rl.lastReq[key] = now
		return true
	}

	elapsed := now.Sub(rl.lastReq[key]).Seconds()
	rl.tokens[key] += elapsed * (rl.maxRate / rl.per.Seconds())
	if rl.tokens[key] > rl.maxRate {
		rl.tokens[key] = rl.maxRate
	}

	if rl.tokens[key] < 1 {
		return false
	}

	rl.tokens[key]--
	rl.lastReq[key] = now
	return true
}

// ==============================
// Distributed Rate Limiting
// ==============================

// distributedAllow implements rate limiting using Redis as a distributed store.
func (rl *defaultRateLimiter) distributedAllow(key string) bool {
	redisKey := fmt.Sprintf("%s:%s", rl.keyPrefix, key)
	now := time.Now().UnixNano()
	windowStart := now - rl.per.Nanoseconds()

	// Remove old entries.
	if err := rl.redisClient.ZRemRangeByScore(redisKey,
		"0",
		strconv.FormatInt(windowStart, 10)); err != nil {
		rl.logger.Error("Failed to remove old entries",
			"error", err,
			"key", key)
		return rl.localAllow(key)
	}

	// Add the new request.
	if err := rl.redisClient.ZAdd(redisKey,
		float64(now),
		strconv.FormatInt(now, 10)); err != nil {
		rl.logger.Error("Failed to add new request",
			"error", err,
			"key", key)
		return rl.localAllow(key)
	}

	// Get the current count.
	count, err := rl.redisClient.ZCount(redisKey,
		strconv.FormatInt(windowStart, 10),
		"+inf")
	if err != nil {
		rl.logger.Error("Failed to get request count",
			"error", err,
			"key", key)
		return rl.localAllow(key)
	}

	// Set an expiration on the key.
	rl.redisClient.Set(redisKey+":exp", "", rl.per*2)

	if rl.metrics != nil {
		rl.metrics.RecordValue("rate_limiter.requests", float64(count),
			map[string]string{"key": key})
	}

	allowed := count <= int64(rl.maxRate)
	if !allowed && rl.metrics != nil {
		rl.metrics.IncrementCounter("rate_limiter.exceeded",
			map[string]string{"key": key})
	}

	return allowed
}

// ==============================
// Quota, Reset, and Cleanup
// ==============================

// GetQuota returns the number of tokens remaining and the time until the next token.
func (rl *defaultRateLimiter) GetQuota(key string) (remaining int, reset time.Time) {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	bucket, exists := rl.buckets[key]
	if !exists {
		return int(rl.maxRate), time.Now()
	}

	currentTokens := bucket.tokens.Load()
	lastRefillTime := time.Unix(0, bucket.lastRefill.Load())

	remainingTokens := float64(currentTokens) / tokenScale

	// Calculate time until the next token is added.
	tokensPerNano := (rl.maxRate / rl.per.Seconds()) / float64(time.Second.Nanoseconds())
	timeToNextToken := time.Duration(float64(tokenScale) / (tokensPerNano * float64(time.Second.Nanoseconds())))

	return int(remainingTokens), lastRefillTime.Add(timeToNextToken)
}

// Reset clears the state associated with a key.
func (rl *defaultRateLimiter) Reset(key string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	delete(rl.buckets, key)
	delete(rl.tokens, key)
	delete(rl.lastReq, key)

	if rl.redisClient != nil {
		redisKey := fmt.Sprintf("%s:%s", rl.keyPrefix, key)
		if err := rl.redisClient.Set(redisKey, "", 0); err != nil {
			rl.logger.Error("Failed to reset Redis key",
				"error", err,
				"key", key)
		}
	}

	if rl.metrics != nil {
		rl.metrics.IncrementCounter("rate_limiter.reset",
			map[string]string{
				"key":    key,
				"source": "api_call",
			})
	}
}

// cleanup periodically removes stale buckets.
func (rl *defaultRateLimiter) cleanup() {
	for range rl.cleanupTick.C {
		rl.mu.Lock()
		now := time.Now()
		for key, bucket := range rl.buckets {
			lastRefillTime := time.Unix(0, bucket.lastRefill.Load())
			if now.Sub(lastRefillTime) > rl.per*2 {
				delete(rl.buckets, key)
				if rl.metrics != nil {
					rl.metrics.IncrementCounter("rate_limiter.bucket_cleaned",
						map[string]string{"key": key})
				}
			}
		}
		rl.mu.Unlock()
	}
}

// ==============================
// Constructor and Options
// ==============================

// NewDefaultRateLimiter creates a new rate limiter and starts its cleanup loop.
func NewDefaultRateLimiter(reqs int, per time.Duration, opts ...RateLimiterOption) RateLimiter {
	rl := &defaultRateLimiter{
		tokens:      make(map[string]float64),
		lastReq:     make(map[string]time.Time),
		buckets:     make(map[string]*tokenBucket),
		maxRate:     float64(reqs),
		per:         per,
		cleanupTick: time.NewTicker(5 * time.Minute),
		keyPrefix:   "ratelimit",
		logger:      NewDefaultLogger(),
		debugMode:   true,
	}

	// Apply options.
	for _, opt := range opts {
		opt(rl)
	}

	go rl.cleanup()
	return rl
}

type RateLimiterOption func(*defaultRateLimiter)

func WithMetrics(metrics MetricsCollector) RateLimiterOption {
	return func(rl *defaultRateLimiter) {
		rl.metrics = metrics
	}
}

// WithRateLimiter sets a custom rate limiter implementation.
func WithRateLimiter(rl RateLimiter) MiddlewareOption {
	return func(m *standardMiddleware) {
		m.rateLimiter = rl
	}
}

// GetDebugStats returns detailed debugging statistics for the specified key.
func (rl *defaultRateLimiter) GetDebugStats(key string) map[string]interface{} {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	bucket, exists := rl.buckets[key]
	if !exists {
		return map[string]interface{}{
			"exists": false,
		}
	}

	return map[string]interface{}{
		"exists":         true,
		"current_tokens": float64(bucket.tokens.Load()) / tokenScale,
		"allow_count":    bucket.stats.allowCount.Load(),
		"reject_count":   bucket.stats.rejectCount.Load(),
		"refill_count":   bucket.stats.refillCount.Load(),
		"last_refill":    time.Unix(0, bucket.lastRefill.Load()),
		"max_rate":       rl.maxRate,
		"period_seconds": rl.per.Seconds(),
	}
}
