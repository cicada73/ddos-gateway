// Package limiter's anomaly detection. This is a different kind of check
// than the token bucket: the token bucket asks "is THIS identity sending
// too many requests?" - this asks "does this identity's BEHAVIOR look like
// an attack pattern, even if no single request rate looks excessive?"
//
// Specifically: IdentityCycleDetector flags a primary identity (e.g. an IP)
// that's observed using an unusually high number of distinct secondary
// identities (e.g. API keys) within a short window. A plain rate limiter
// is blind to this - an attacker spreading 100 requests across 25 different
// API keys (4 requests each) never gets close to any single key's limit,
// but the pattern of "one IP, 25 keys, all within a minute" is exactly
// what credential-stuffing and key-cycling attacks look like.
package limiter

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// IdentityCycleDetector tracks, per primary identity, the set of distinct
// secondary identities seen recently, and flags the primary identity once
// that set grows past a threshold.
type IdentityCycleDetector struct {
	client     *redis.Client
	setPrefix  string
	flagPrefix string
	window     time.Duration
	threshold  int64
	cooldown   time.Duration
}

// NewIdentityCycleDetector: window is how far back "recently" looks (the
// tracking set's TTL is refreshed on every observation, so it's a rolling
// window of recent activity rather than a fixed calendar window). threshold
// is how many distinct secondary identities are allowed before flagging.
// cooldown is how long a flag persists once triggered - the primary
// identity is blocked entirely for this long, not just until the window
// resets, since a pattern that anomalous deserves more than a brief pause.
func NewIdentityCycleDetector(client *redis.Client, prefix string, window time.Duration, threshold int64, cooldown time.Duration) *IdentityCycleDetector {
	return &IdentityCycleDetector{
		client:     client,
		setPrefix:  "anomaly:" + prefix + ":set:",
		flagPrefix: "anomaly:" + prefix + ":flag:",
		window:     window,
		threshold:  threshold,
		cooldown:   cooldown,
	}
}

// RecordAndCheck records that `primary` was just observed using `secondary`,
// and reports whether `primary` should be treated as anomalous right now.
// Returns (flagged, distinctSecondaryCount, error).
func (d *IdentityCycleDetector) RecordAndCheck(ctx context.Context, primary, secondary string) (bool, int64, error) {
	flagKey := d.flagPrefix + primary

	// Already flagged from a prior request within its cooldown? Short-circuit
	// without touching the tracking set at all - once flagged, every request
	// from this identity is blocked until the flag expires.
	exists, err := d.client.Exists(ctx, flagKey).Result()
	if err != nil {
		return false, 0, fmt.Errorf("anomaly: exists check: %w", err)
	}
	if exists == 1 {
		return true, 0, nil
	}

	setKey := d.setPrefix + primary
	pipe := d.client.TxPipeline()
	pipe.SAdd(ctx, setKey, secondary)
	pipe.Expire(ctx, setKey, d.window)
	cardCmd := pipe.SCard(ctx, setKey)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, 0, fmt.Errorf("anomaly: pipeline: %w", err)
	}

	count := cardCmd.Val()
	if count > d.threshold {
		if err := d.client.Set(ctx, flagKey, "1", d.cooldown).Err(); err != nil {
			return true, count, fmt.Errorf("anomaly: set flag: %w", err)
		}
		return true, count, nil
	}
	return false, count, nil
}
