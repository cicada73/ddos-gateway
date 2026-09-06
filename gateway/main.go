package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	"github.com/redis/go-redis/v9"

	"ddos-gateway/gateway/limiter"
)

// clientKey decides how we identify a client for rate-limiting purposes.
// Authenticated clients (carrying X-API-Key) are limited per API key;
// anonymous clients are limited per source IP. This mirrors real gateways,
// and gives the Phase 4 anomaly scoring more signal to work with later
// (e.g. an IP cycling through many API keys looks very different from one
// legitimate key making steady requests).
func clientKey(r *http.Request) string {
	if apiKey := r.Header.Get("X-API-Key"); apiKey != "" {
		return "key:" + apiKey
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "ip:" + host
}

// rateLimitMiddleware wraps a handler, rejecting requests with 429 once a
// client's bucket is empty. It also sets informational headers so a client
// (or our attack simulator/dashboard) can see how close it is to the limit.
//
// Fail-open policy: if Redis itself is unreachable, we log the error and
// let the request through rather than blocking all traffic. The trade-off:
// a Redis outage means temporarily unlimited traffic (bad) rather than
// every legitimate user being locked out because the limiter's dependency
// died (worse for availability). This is the same choice most production
// rate limiters make - but it's a real design decision, not an accident,
// and it's worth being able to explain the alternative (fail-closed) and
// why we didn't pick it here.
func rateLimitMiddleware(next http.Handler, l *limiter.RedisTokenBucketLimiter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := clientKey(r)
		allowed, remaining, err := l.Allow(r.Context(), key)

		if err != nil {
			log.Printf("LIMITER ERROR (failing open) key=%s err=%v", key, err)
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%.0f", remaining))

		if !allowed {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			log.Printf("BLOCKED key=%s path=%s", key, r.URL.Path)
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

	handler := rateLimitMiddleware(proxy, tb)

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
