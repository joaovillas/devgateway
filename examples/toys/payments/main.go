// Comando payments: serviço de pagamentos de brinquedo para o ambiente de
// exemplo. Responde JSON, tem um endpoint lento (a criação de cobrança) e um
// fluxo SSE de eventos. Nada é persistido: as cobranças vivem na memória.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type charge struct {
	ID        string    `json:"id"`
	Amount    int       `json:"amount"`
	Currency  string    `json:"currency"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

type store struct {
	mu      sync.Mutex
	next    int
	charges map[string]charge
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9001", "endereço em que o serviço atende")
	flag.Parse()

	s := &store{charges: map[string]charge{}}
	s.add(4990, "BRL")
	s.add(12000, "BRL")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "payments"})
	})
	mux.HandleFunc("GET /balance", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": map[string]any{"amount": 152340, "currency": "BRL"},
			"pending":   map[string]any{"amount": 8800, "currency": "BRL"},
		})
	})
	mux.HandleFunc("GET /charges", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": s.list()})
	})
	mux.HandleFunc("GET /charges/{id}", func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.get(r.PathValue("id"))
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "charge_not_found"})
			return
		}
		writeJSON(w, http.StatusOK, c)
	})
	// A criação é o endpoint lento: simula a ida a um adquirente, entre
	// 400 ms e 1,2 s.
	mux.HandleFunc("POST /charges", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Amount   int    `json:"amount"`
			Currency string `json:"currency"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Amount <= 0 {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "invalid_amount"})
			return
		}
		if in.Currency == "" {
			in.Currency = "BRL"
		}
		select {
		case <-time.After(400*time.Millisecond + rand.N(800*time.Millisecond)):
		case <-r.Context().Done():
			return
		}
		writeJSON(w, http.StatusCreated, s.add(in.Amount, in.Currency))
	})
	// Fluxo SSE: um evento por segundo até o cliente desconectar.
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming não suportado", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, ": fluxo de eventos de pagamento\n\n")
		fl.Flush()
		kinds := []string{"charge.created", "charge.paid", "charge.refunded", "payout.sent"}
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for seq := 1; ; seq++ {
			select {
			case <-r.Context().Done():
				return
			case now := <-t.C:
				data, _ := json.Marshal(map[string]any{
					"seq": seq, "amount": 100 * (1 + rand.N(500)), "at": now.UTC(),
				})
				fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", seq, kinds[rand.N(len(kinds))], data)
				fl.Flush()
			}
		}
	})

	log.Printf("payments atendendo em http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func (s *store) add(amount int, currency string) charge {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	c := charge{
		ID:        "ch_" + strconv.Itoa(s.next),
		Amount:    amount,
		Currency:  currency,
		Status:    "paid",
		CreatedAt: time.Now().UTC(),
	}
	s.charges[c.ID] = c
	return c
}

func (s *store) get(id string) (charge, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.charges[id]
	return c, ok
}

func (s *store) list() []charge {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]charge, 0, len(s.charges))
	for i := 1; i <= s.next; i++ {
		if c, ok := s.charges["ch_"+strconv.Itoa(i)]; ok {
			out = append(out, c)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
