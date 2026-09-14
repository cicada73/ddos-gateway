package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Item is a trivial resource so we have something real to protect.
type Item struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

var (
	mu     sync.Mutex
	items  = []Item{{ID: 1, Name: "sample-item"}}
	nextID = 2
)

// simulateWork adds a small random delay before handling any request, so the
// backend behaves like a real app doing real work (a DB query, a template
// render) instead of an instant echo. This matters specifically for the
// visual/browser demo: an instantly-responding backend can absorb thousands
// of attack requests without ever looking stressed, which is technically
// correct but doesn't SHOW anything. A believable bit of latency is what
// makes an unprotected page visibly bog down under load, while the
// protected one (which never lets that load reach here) stays snappy.
//
// This has zero effect on rate-limiting decisions themselves - those happen
// in the gateway, before a request ever reaches the backend.
// workSlots simulates a backend with FINITE capacity - like a limited
// worker pool, thread pool, or database connection pool, which almost
// every real backend has. Without this, Go's http.Server would happily
// process thousands of concurrent requests in parallel and the demo would
// show no visible difference under load, which is not representative of
// how most real services actually behave under a genuine attack.
var workSlots = make(chan struct{}, 8) // at most 8 requests processed at once

func simulateWork() {
	workSlots <- struct{}{}                                        // blocks here if 8 requests are already in flight
	defer func() { <-workSlots }()                                 // release the slot when done
	time.Sleep(time.Duration(50+rand.Intn(50)) * time.Millisecond) // 50-100ms
}

func itemsHandler(w http.ResponseWriter, r *http.Request) {
	simulateWork()
	switch r.Method {
	case http.MethodGet:
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)

	case http.MethodPost:
		var newItem Item
		if err := json.NewDecoder(r.Body).Decode(&newItem); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		mu.Lock()
		newItem.ID = nextID
		nextID++
		items = append(items, newItem)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(newItem)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	// Deliberately NOT calling simulateWork() here - health checks should
	// always be instant, that's the whole point of a health endpoint.
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// homepageHandler serves a small dummy storefront page - purely for the
// visual side of the demo (a browser tab that visibly stays responsive
// behind the gateway vs. one that bogs down without it). It reads the
// current item count so the page has SOMETHING dynamic tying it back to
// the same backend state /items uses, rather than being a static mockup.
func homepageHandler(w http.ResponseWriter, r *http.Request) {
	simulateWork()
	mu.Lock()
	itemCount := len(items)
	mu.Unlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := `<!DOCTYPE html>
<html>
<head>
<title>Acme Storefront (demo)</title>
<style>
  body { font-family: -apple-system, sans-serif; max-width: 640px; margin: 60px auto; padding: 0 20px; color: #222; }
  h1 { margin-bottom: 4px; }
  .tagline { color: #666; margin-top: 0; }
  .card { border: 1px solid #ddd; border-radius: 8px; padding: 20px; margin-top: 24px; }
  .stamp { color: #888; font-size: 13px; margin-top: 24px; }
</style>
</head>
<body>
  <h1>Acme Storefront</h1>
  <p class="tagline">A dummy website used to demo gateway protection.</p>
  <div class="card">
    <strong>Catalog status:</strong> ` + http.StatusText(http.StatusOK) + `<br>
    <strong>Items in stock:</strong> ` + strconv.Itoa(itemCount) + `
  </div>
  <p class="stamp">Served at ` + time.Now().Format("15:04:05.000") + ` &mdash; if this page is loading slowly or timing out, the backend is under load with no protection in front of it.</p>
</body>
</html>`
	w.Write([]byte(html))
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", homepageHandler)
	mux.HandleFunc("/items", itemsHandler)
	mux.HandleFunc("/health", healthHandler)

	addr := ":9000"
	log.Printf("backend listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
