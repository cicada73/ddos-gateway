// Package limiter implements rate-limiting algorithms for the gateway.
//
// Phase 1: in-memory token bucket, one bucket per client key (IP or API key).
// Phase 2 will swap the storage for Redis so multiple gateway replicas share
// state - but the Allow() interface stays the same, which is why it's
// worth getting this abstraction right now.
package limiter

import (
	"sync"
	"time"
)

// bucket holds the state for a single client's token bucket.
type bucket struct {
	tokens     float64
	lastRefill time.Time
}

// TokenBucketLimiter implements the token bucket algorithm:
// each client gets a bucket that holds up to `capacity` tokens.
// Tokens refill continuously at `refillRate` tokens/second.
// Each request costs 1 token. If the bucket is empty, the request is denied.
//
// Why token bucket for v1: it naturally allows short bursts (as long as
// tokens have accumulated) while still enforcing a long-run average rate -
// which is closer to real traffic patterns than a hard fixed-window cap.
type TokenBucketLimiter struct {
	mu         sync.Mutex
	buckets    map[string]*bucket
	capacity   float64
	refillRate float64 // tokens per second
}

// NewTokenBucketLimiter creates a limiter where each client can burst up to
// `capacity` requests, and the bucket refills at `refillRate` tokens/sec.
func NewTokenBucketLimiter(capacity, refillRate float64) *TokenBucketLimiter {
	return &TokenBucketLimiter{
		buckets:    make(map[string]*bucket),
		capacity:   capacity,
		refillRate: refillRate,
	}
}

// Allow reports whether the request identified by `key` (an IP or API key)
// should be permitted right now. It also returns the number of tokens
// remaining after this decision, which we'll surface as a response header.
func (l *TokenBucketLimiter) Allow(key string) (allowed bool, remaining float64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, exists := l.buckets[key]
	if !exists {
		// First time we've seen this client: start with a full bucket.
		b = &bucket{tokens: l.capacity, lastRefill: now}
		l.buckets[key] = b
	}

	// Refill based on elapsed time since we last touched this bucket.
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * l.refillRate
	if b.tokens > l.capacity {
		b.tokens = l.capacity
	}
	b.lastRefill = now

	if b.tokens >= 1 {
		b.tokens -= 1
		return true, b.tokens
	}
	return false, b.tokens
}