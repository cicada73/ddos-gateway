package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"

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
func rateLimitMiddleware(next http.Handler, l *limiter.TokenBucketLimiter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := clientKey(r)
		allowed, remaining := l.Allow(key)

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
	backendURL, err := url.Parse("http://localhost:9000")
	if err != nil {
		log.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(backendURL)

	// Capacity 20, refill 5 tokens/sec => sustained ~5 req/s per client,
	// with room to burst up to 20 at once. Tune these once you see real
	// traffic from the Phase 3 simulator.
	tb := limiter.NewTokenBucketLimiter(20, 5)

	handler := rateLimitMiddleware(proxy, tb)

	port := 8080
	log.Printf("gateway listening on :%d, proxying to %s", port, backendURL)
	log.Fatal(http.ListenAndServe(":"+strconv.Itoa(port), handler))
}
