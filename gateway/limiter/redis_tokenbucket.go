// Package limiter's Redis-backed implementation. Same idea as the in-memory
// TokenBucketLimiter from Phase 1, but the bucket state lives in Redis
// instead of a local Go map - so every gateway replica sees the same
// buckets, instead of each replica keeping (and being fooled by) its own.
package limiter

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/redis/go-redis/v9"
)

//go:embed tokenbucket.lua
var tokenBucketScript string

// RedisTokenBucketLimiter implements the same token bucket algorithm as
// Phase 1's in-memory version, but delegates the actual check-and-consume
// to a Lua script running inside Redis, so it's safe to call concurrently
// from multiple gateway processes.
type RedisTokenBucketLimiter struct {
	client     *redis.Client
	script     *redis.Script
	capacity   float64
	refillRate float64
	ttlSeconds int
}

// NewRedisTokenBucketLimiter mirrors NewTokenBucketLimiter's parameters
// (capacity, refillRate) plus a Redis client. ttlSeconds controls how long
// an idle client's bucket persists in Redis before being cleaned up
// automatically - it does not affect rate-limiting behavior, only memory
// usage in Redis over time.
func NewRedisTokenBucketLimiter(client *redis.Client, capacity, refillRate float64) *RedisTokenBucketLimiter {
	return &RedisTokenBucketLimiter{
		client:     client,
		script:     redis.NewScript(tokenBucketScript),
		capacity:   capacity,
		refillRate: refillRate,
		ttlSeconds: 3600,
	}
}

// Allow reports whether the request identified by key should be permitted.
// Unlike Phase 1's in-memory version, this can fail (e.g. Redis is
// unreachable), so callers must handle the error - see the fail-open
// discussion in gateway/main.go for how the gateway responds to that case.
func (l *RedisTokenBucketLimiter) Allow(ctx context.Context, key string) (allowed bool, remaining float64, err error) {
	res, err := l.script.Run(ctx, l.client,
		[]string{"ratelimit:" + key},
		l.capacity, l.refillRate, 1, l.ttlSeconds,
	).Result()
	if err != nil {
		return false, 0, fmt.Errorf("redis token bucket: %w", err)
	}

	arr, ok := res.([]interface{})
	if !ok || len(arr) != 2 {
		return false, 0, fmt.Errorf("redis token bucket: unexpected script result %v", res)
	}

	allowedInt, _ := arr[0].(int64)
	var rem float64
	if s, ok := arr[1].(string); ok {
		fmt.Sscanf(s, "%f", &rem)
	}

	return allowedInt == 1, rem, nil
}
