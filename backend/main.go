package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
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

func itemsHandler(w http.ResponseWriter, r *http.Request) {
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
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/items", itemsHandler)
	mux.HandleFunc("/health", healthHandler)

	addr := ":9000"
	log.Printf("backend listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
