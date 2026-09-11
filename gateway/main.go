package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	"ddos-gateway/gateway/limiter"
)

// extractIdentity pulls both the source IP and (if present) the API key
// from a request. Phase 1-2 only needed one combined "key" - Phase 4 needs
// both separately, since anomaly detection is specifically about the
// RELATIONSHIP between the two (how many keys does this IP use? how many
// IPs does this key show up from?), not just picking one as the identity.
func extractIdentity(r *http.Request) (rateLimitKey, ip, apiKey string) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip = host
	apiKey = r.Header.Get("X-API-Key")

	if apiKey != "" {
		rateLimitKey = "key:" + apiKey
	} else {
		rateLimitKey = "ip:" + ip
	}
	return
}

// gatewayMiddleware runs two independent checks before proxying a request:
//
//  1. Anomaly detection: does this IP's or key's BEHAVIOR look like an
//     attack pattern (key cycling / credential stuffing), regardless of
//     whether any single identity has hit its rate limit? Checked first,
//     because a flagged identity should be blocked outright, not just
//     rate-limited.
//  2. Token bucket: is THIS identity individually sending too many
//     requests right now? (Phase 1-2's check, unchanged.)
//
// Fail-open policy applies to both: if Redis itself is unreachable, we log
// the error and let the request through rather than blocking all traffic.
// The trade-off: a Redis outage means temporarily unlimited/unscored
// traffic (bad) rather than every legitimate user being locked out because
// the gateway's dependency died (worse for availability).
func gatewayMiddleware(next http.Handler, tb *limiter.RedisTokenBucketLimiter, ipCycle, keyCycle *limiter.IdentityCycleDetector) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rateLimitKey, ip, apiKey := extractIdentity(r)
		ctx := r.Context()

		if apiKey != "" {
			ipFlagged, ipDistinctKeys, err := ipCycle.RecordAndCheck(ctx, ip, apiKey)
			if err != nil {
				log.Printf("ANOMALY ERROR (failing open) ip=%s err=%v", ip, err)
			} else if ipFlagged {
				http.Error(w, "request pattern flagged as anomalous", http.StatusForbidden)
				log.Printf("ANOMALY BLOCKED ip=%s reason=key_cycling distinct_keys=%d", ip, ipDistinctKeys)
				return
			}

			keyFlagged, keyDistinctIPs, err := keyCycle.RecordAndCheck(ctx, apiKey, ip)
			if err != nil {
				log.Printf("ANOMALY ERROR (failing open) key=%s err=%v", apiKey, err)
			} else if keyFlagged {
				http.Error(w, "request pattern flagged as anomalous", http.StatusForbidden)
				log.Printf("ANOMALY BLOCKED key=%s reason=ip_cycling distinct_ips=%d", apiKey, keyDistinctIPs)
				return
			}
		}

		allowed, remaining, err := tb.Allow(ctx, rateLimitKey)
		if err != nil {
			log.Printf("LIMITER ERROR (failing open) key=%s err=%v", rateLimitKey, err)
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%.0f", remaining))

		if !allowed {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			log.Printf("BLOCKED key=%s path=%s", rateLimitKey, r.URL.Path)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func main() {
	backendAddr := getenv("BACKEND_URL", "http://localhost:9000")
	backendURL, err := url.Parse(backendAddr)
	if err != nil {
		log.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(backendURL)

	redisAddr := getenv("REDIS_ADDR", "localhost:6379")
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})

	// Capacity 20, refill 5 tokens/sec => sustained ~5 req/s per client,
	// with room to burst up to 20 at once. Same numbers as Phase 1 - what
	// changes is that this state is now shared across every replica.
	tb := limiter.NewRedisTokenBucketLimiter(rdb, 20, 5)

	// Anomaly detection: an IP using more than 5 distinct API keys within
	// 60 seconds gets flagged (key cycling / credential stuffing), as does
	// an API key seen from more than 5 distinct IPs within 60 seconds
	// (a shared/stolen key or a distributed attack). Flags last 2 minutes.
	ipCycle := limiter.NewIdentityCycleDetector(rdb, "ip-keys", 60*time.Second, 5, 2*time.Minute)
	keyCycle := limiter.NewIdentityCycleDetector(rdb, "key-ips", 60*time.Second, 5, 2*time.Minute)

	handler := gatewayMiddleware(proxy, tb, ipCycle, keyCycle)

	port := getenv("PORT", "8080")
	replicaID := getenv("REPLICA_ID", "unknown")
	log.Printf("gateway[%s] listening on :%s, proxying to %s, redis at %s", replicaID, port, backendURL, redisAddr)
	log.Fatal(http.ListenAndServe(":"+port, handler))
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
