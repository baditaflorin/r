package r

import (
	"fmt"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// tokenBucket stores the rate limiting state
type tokenBucket struct {
	tokens     atomic.Int64 // Store tokens * 1_000_000
	lastRefill atomic.Int64 // Store UnixNano timestamp
	stats      struct {
		allowCount  atomic.Int64
		rejectCount atomic.Int64
		refillCount atomic.Int64
	}
}

const tokenScale = 1_000_000 // Scale factor for fixed-point arithmetic

type defaultRateLimiter struct {
	tokens      map[string]float64
	lastReq     map[string]time.Time
	maxRate     float64
	per         time.Duration
	mu          sync.RWMutex
	cleanupTick *time.Ticker
	redisClient RedisClient
	keyPrefix   string
	logger      Logger
	metrics     MetricsCollector
	buckets     map[string]*tokenBucket
	debugMode   bool
}

func (rl *defaultRateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()

	bucket, exists := rl.buckets[key]
	if !exists {
		// First request, create new bucket with full tokens
		bucket = &tokenBucket{}
		bucket.tokens.Store(int64(rl.maxRate * tokenScale))
		bucket.lastRefill.Store(now.UnixNano())
		rl.buckets[key] = bucket
		bucket.stats.allowCount.Add(1)

		if rl.metrics != nil {
			rl.metrics.IncrementCounter("rate_limiter.bucket_created",
				map[string]string{"key": key})
		}
		return true
	}

	// Calculate elapsed time since last refill with nanosecond precision
	lastRefillTime := time.Unix(0, bucket.lastRefill.Load())
	elapsed := now.Sub(lastRefillTime).Seconds()

	// Calculate tokens to add with high precision
	currentTokens := float64(bucket.tokens.Load()) / tokenScale
	tokensToAdd := elapsed * (rl.maxRate / rl.per.Seconds())

	// Add tokens and round up to prevent token loss from floating point rounding
	newTokens := math.Min(rl.maxRate, currentTokens+tokensToAdd)

	// Store new token count with scaling
	bucket.tokens.Store(int64(newTokens * tokenScale))
	bucket.lastRefill.Store(now.UnixNano())
	bucket.stats.refillCount.Add(1)

	// We need at least 1.0 tokens to allow the request
	if newTokens >= 1.0 {
		// Consume exactly one token
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

// Add distributed implementation
func (rl *defaultRateLimiter) distributedAllow(key string) bool {
	redisKey := fmt.Sprintf("%s:%s", rl.keyPrefix, key)

	// Use simpler implementation with existing RedisClient interface
	now := time.Now().UnixNano()
	windowStart := now - rl.per.Nanoseconds()

	// Remove old entries
	if err := rl.redisClient.ZRemRangeByScore(redisKey,
		"0",
		strconv.FormatInt(windowStart, 10)); err != nil {
		rl.logger.Error("Failed to remove old entries",
			"error", err,
			"key", key)
		return rl.localAllow(key)
	}

	// Add new request
	if err := rl.redisClient.ZAdd(redisKey,
		float64(now),
		strconv.FormatInt(now, 10)); err != nil {
		rl.logger.Error("Failed to add new request",
			"error", err,
			"key", key)
		return rl.localAllow(key)
	}

	// Get current count
	count, err := rl.redisClient.ZCount(redisKey,
		strconv.FormatInt(windowStart, 10),
		"+inf")
	if err != nil {
		rl.logger.Error("Failed to get request count",
			"error", err,
			"key", key)
		return rl.localAllow(key)
	}

	// Set expiration using existing Set method
	rl.redisClient.Set(redisKey+":exp", "", rl.per*2)

	// Update metrics if available
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

func (rl *defaultRateLimiter) GetQuota(key string) (remaining int, reset time.Time) {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	bucket, exists := rl.buckets[key]
	if !exists {
		return int(rl.maxRate), time.Now()
	}

	currentTokens := bucket.tokens.Load()
	lastRefillTime := time.Unix(0, bucket.lastRefill.Load())

	// Calculate tokens available
	remainingTokens := float64(currentTokens) / tokenScale

	// Calculate time until next token
	tokensPerNano := (rl.maxRate / rl.per.Seconds()) / float64(time.Second.Nanoseconds())
	timeToNextToken := time.Duration(float64(tokenScale) / (tokensPerNano * float64(time.Second.Nanoseconds())))

	return int(remainingTokens), lastRefillTime.Add(timeToNextToken)
}

func (rl *defaultRateLimiter) Reset(key string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Clean up the bucket
	delete(rl.buckets, key)

	// Clean up legacy maps for backward compatibility
	delete(rl.tokens, key)
	delete(rl.lastReq, key)

	// If using Redis, clean up distributed state
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

func (rl *defaultRateLimiter) cleanup() {
	for range rl.cleanupTick.C {
		rl.mu.Lock()
		now := time.Now()
		for key, bucket := range rl.buckets {
			// Convert atomic timestamp to time.Time
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

// Update constructor to include new fields
func NewDefaultRateLimiter(reqs int, per time.Duration, opts ...RateLimiterOption) RateLimiter {
	rl := &defaultRateLimiter{
		tokens:      make(map[string]float64),
		lastReq:     make(map[string]time.Time),
		maxRate:     float64(reqs),
		per:         per,
		cleanupTick: time.NewTicker(time.Minute * 5),
		keyPrefix:   "ratelimit",
		logger:      NewDefaultLogger(),
		buckets:     make(map[string]*tokenBucket),
		debugMode:   true, // Enable debug by default for investigation
	}

	// Apply options
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

// WithRateLimiter sets a custom rate limiter implementation
func WithRateLimiter(rl RateLimiter) MiddlewareOption {
	return func(m *standardMiddleware) {
		m.rateLimiter = rl
	}
}

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
