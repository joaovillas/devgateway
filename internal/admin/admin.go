// Package admin atende a porta de administração: API REST e interface web.
package admin

import (
	"encoding/json"
	"io/fs"
	"net/http"

	"github.com/gamerjp64/gateway/internal/config"
)

// Handler é o handler da porta de administração.
type Handler struct {
	live *config.Live
	mux  *http.ServeMux
}

func NewHandler(live *config.Live, web fs.FS) *Handler {
	h := &Handler{live: live, mux: http.NewServeMux()}
	h.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "recurso da API inexistente: "+r.URL.Path)
	})
	h.mux.Handle("/", spa(web))
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// spa serve o frontend embutido; paths que não são arquivos caem no
// index.html, para que a navegação do lado do cliente funcione.
func spa(web fs.FS) http.Handler {
	files := http.FileServerFS(web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path[1:]
		if p != "" {
			if st, err := fs.Stat(web, p); err != nil || st.IsDir() {
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/"
				files.ServeHTTP(w, r2)
				return
			}
		}
		files.ServeHTTP(w, r)
	})
}

type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Error: code, Message: msg})
}
