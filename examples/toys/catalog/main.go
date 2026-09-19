// Command catalog is a toy product catalog for the example environment.
// It answers JSON; the stock lookup fails now and then (the fraction is
// tunable with -fail) and search is a little slow.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

type product struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Price int    `json:"price"`
}

var products = []product{
	{ID: "p1", Name: "Enamel mug", Price: 4990},
	{ID: "p2", Name: "Dotted notebook", Price: 3200},
	{ID: "p3", Name: "Mechanical pencil 0.5 mm", Price: 2750},
	{ID: "p4", Name: "Vacuum flask", Price: 8900},
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9002", "address the service listens on")
	fail := flag.Float64("fail", 0.25, "fraction of stock lookups that fail with 503")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "catalog"})
	})
	mux.HandleFunc("GET /products", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": products})
	})
	mux.HandleFunc("GET /products/{id}", func(w http.ResponseWriter, r *http.Request) {
		if p, ok := find(r.PathValue("id")); ok {
			writeJSON(w, http.StatusOK, p)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "product_not_found"})
	})
	// Stock is the flaky endpoint: a fraction of the lookups fail.
	mux.HandleFunc("GET /stock/{id}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := find(r.PathValue("id")); !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "product_not_found"})
			return
		}
		if rand.Float64() < *fail {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "stock_backend_unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "available": rand.N(40)})
	})
	// Search takes between 150 and 450 ms.
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(150*time.Millisecond + rand.N(300*time.Millisecond)):
		case <-r.Context().Done():
			return
		}
		q := strings.ToLower(r.URL.Query().Get("q"))
		out := []product{}
		for _, p := range products {
			if strings.Contains(strings.ToLower(p.Name), q) {
				out = append(out, p)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"query": q, "data": out})
	})

	log.Printf("catalog listening on http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func find(id string) (product, bool) {
	for _, p := range products {
		if p.ID == id {
			return p, true
		}
	}
	return product{}, false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
