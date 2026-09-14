package main

import (
	"encoding/json"
	"html"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
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
//
// Capacity 3 + ~150-300ms per request models a modest real backend (a
// template render plus a couple of DB round trips). At this ceiling,
// roughly 3/0.2s =~ 15 req/s sustained throughput - a sustained attack of
// even a handful of concurrent connections exceeds that, so requests
// queue up and per-request latency climbs into multi-second territory
// instead of staying flat. This was tuned and verified against a real
// Locust run before being handed off - see docs/PROJECT_EXPLAINER.md for
// the measured numbers.
var workSlots = make(chan struct{}, 8)

func simulateWork() {
	workSlots <- struct{}{}
	defer func() { <-workSlots }()
	time.Sleep(time.Duration(150+rand.Intn(150)) * time.Millisecond) // 150-300ms
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
// behind the gateway vs. one that bogs down without it). It lists the
// actual current items from backend state, so the page reflects real
// server state rather than being a static mockup.
func homepageHandler(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	simulateWork()
	responseMs := time.Since(start).Milliseconds()

	mu.Lock()
	currentItems := make([]Item, len(items))
	copy(currentItems, items)
	mu.Unlock()

	var rows strings.Builder
	for _, it := range currentItems {
		rows.WriteString(`<li><span class="dot"></span>` + html.EscapeString(it.Name) + `</li>`)
	}

	status := "quiet"
	if responseMs > 400 {
		status = "a busy moment"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Kettle &amp; Vine</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link href="https://fonts.googleapis.com/css2?family=Fraunces:opsz,wght@9..144,500&family=Inter:wght@400;500&display=swap" rel="stylesheet">
<style>
  :root {
    --paper: #F3F5EF;
    --ink: #1F2A22;
    --ochre: #B8863B;
    --sage: #7A8B6F;
    --line: #DCE2D5;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    background: var(--paper);
    color: var(--ink);
    font-family: 'Inter', sans-serif;
    line-height: 1.5;
  }
  .wrap {
    max-width: 480px;
    margin: 0 auto;
    padding: 72px 24px 40px;
  }
  .mark {
    width: 36px; height: 36px;
    border: 1.5px solid var(--ink);
    border-radius: 50%;
    display: flex; align-items: center; justify-content: center;
    font-family: 'Fraunces', serif;
    font-size: 18px;
    margin-bottom: 28px;
  }
  h1 {
    font-family: 'Fraunces', serif;
    font-weight: 500;
    font-size: 42px;
    line-height: 1.05;
    margin: 0 0 10px;
    letter-spacing: -0.01em;
  }
  .tagline {
    color: var(--sage);
    font-size: 16px;
    margin: 0 0 40px;
    max-width: 34ch;
  }
  h2 {
    font-family: 'Fraunces', serif;
    font-weight: 500;
    font-size: 15px;
    letter-spacing: 0.02em;
    color: var(--sage);
    margin: 0 0 14px;
  }
  ul.catalog {
    list-style: none;
    padding: 0;
    margin: 0 0 40px;
    border-top: 1px solid var(--line);
  }
  ul.catalog li {
    padding: 14px 0;
    border-bottom: 1px solid var(--line);
    font-size: 17px;
    display: flex;
    align-items: center;
  }
  .dot {
    width: 6px; height: 6px;
    border-radius: 50%;
    background: var(--ochre);
    margin-right: 12px;
    flex-shrink: 0;
  }
  .receipt {
    border-top: 1px dashed var(--line);
    padding-top: 16px;
    font-size: 13px;
    color: var(--sage);
    display: flex;
    justify-content: space-between;
  }
</style>
</head>
<body>
  <div class="wrap">
    <div class="mark">K&amp;V</div>
    <h1>Kettle &amp; Vine</h1>
    <p class="tagline">Small-batch pantry goods from a few growers we know by name.</p>

    <h2>In the shop today</h2>
    <ul class="catalog">
      ` + rows.String() + `
    </ul>

    <div class="receipt">
      <span>Order desk: ` + status + `</span>
      <span>` + strconv.FormatInt(responseMs, 10) + `ms</span>
    </div>
  </div>
</body>
</html>`
	w.Write([]byte(page))
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
