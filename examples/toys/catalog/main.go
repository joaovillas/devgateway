// Comando catalog: catálogo de produtos de brinquedo para o ambiente de
// exemplo. Responde JSON; a consulta de estoque às vezes falha (fração
// ajustável por -fail) e a busca é um pouco lenta.
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
	{ID: "p1", Name: "Caneca esmaltada", Price: 4990},
	{ID: "p2", Name: "Caderno pontilhado", Price: 3200},
	{ID: "p3", Name: "Lapiseira 0,5 mm", Price: 2750},
	{ID: "p4", Name: "Garrafa térmica", Price: 8900},
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9002", "endereço em que o serviço atende")
	fail := flag.Float64("fail", 0.25, "fração das consultas de estoque que falham com 503")
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
	// O estoque é o endpoint instável: uma fração das consultas falha.
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
	// A busca demora entre 150 e 450 ms.
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

	log.Printf("catalog atendendo em http://%s", *addr)
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
