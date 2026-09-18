// Package admin atende a porta de administração: API REST e interface web.
// O contrato da API está em docs/api.md.
package admin

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/gamerjp64/gateway/internal/capture"
	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/config/writer"
	"github.com/gamerjp64/gateway/internal/override"
	"github.com/gamerjp64/gateway/internal/store"
	"github.com/gamerjp64/gateway/internal/upstream"
)

// Deps reúne o que a API opera.
type Deps struct {
	Live *config.Live
	// Web é o frontend embutido.
	Web fs.FS
	// History é o histórico em uso, com backend trocável a quente.
	History *store.Switchable
	// Recorder registra as trocas; a API espera por ele antes de ler o
	// histórico e o usa para limpá-lo.
	Recorder *capture.Recorder
	// Writer serializa as escritas de documento de rota e a recarga.
	Writer *writer.Writer
	// Overrides é o estado vivo dos overrides.
	Overrides *override.Tracker
	// Upstreams é a disponibilidade recente dos upstreams, alimentada pela
	// porta de tráfego. nil: um acompanhamento próprio, sempre desconhecido.
	Upstreams *upstream.Health
	// Loader relê gateway.json e o diretório de rotas na recarga.
	Loader config.Loader
	// Apply aplica a quente o que a configuração do processo controla fora
	// do snapshot (portas, backend do histórico), antes de o snapshot novo
	// ser publicado. Prepara o que for preciso, chama persist (que grava
	// gateway.json numa alteração pela API; nil na recarga) e só então
	// troca. Um erro recusa a mudança e preserva a configuração em vigor; um
	// *ApplyError escolhe o código da resposta. nil: nada a aplicar além de
	// persist.
	Apply func(old, next config.Settings, persist func() error) error
	// Ports informa as portas em que o processo atende agora, que diferem
	// das configuradas quando a configuração pede a porta 0. nil: as
	// configuradas.
	Ports func() (traffic, admin int)
	// StartedAt é o instante em que o processo subiu.
	StartedAt time.Time
	// Heartbeat é o intervalo do heartbeat do fluxo de eventos; zero usa
	// DefaultHeartbeat.
	Heartbeat time.Duration
}

// Version é a versão do gateway informada pela API.
var Version = "0.1.0"

// Handler é o handler da porta de administração.
type Handler struct {
	live      *config.Live
	history   *store.Switchable
	rec       *capture.Recorder
	writer    *writer.Writer
	overrides *override.Tracker
	upstreams *upstream.Health
	loader    config.Loader
	apply     func(old, next config.Settings, persist func() error) error
	ports     func() (int, int)
	startedAt time.Time
	heartbeat time.Duration
	events    *hub
	now       func() time.Time
	mux       *http.ServeMux
}

// New monta a API sobre as dependências e serve o frontend embutido.
func New(d Deps) *Handler {
	h := &Handler{
		live:      d.Live,
		history:   d.History,
		rec:       d.Recorder,
		writer:    d.Writer,
		overrides: d.Overrides,
		upstreams: d.Upstreams,
		loader:    d.Loader,
		apply:     d.Apply,
		ports:     d.Ports,
		startedAt: d.StartedAt,
		heartbeat: d.Heartbeat,
		events:    newHub(),
		now:       time.Now,
		mux:       http.NewServeMux(),
	}
	if h.upstreams == nil {
		h.upstreams = upstream.New()
	}
	if h.heartbeat <= 0 {
		h.heartbeat = DefaultHeartbeat
	}
	if h.startedAt.IsZero() {
		h.startedAt = h.now()
	}
	if h.loader.Getenv == nil {
		h.loader.Getenv = os.LookupEnv
	}
	// Toda alteração aplicada — pela API, pela recarga ou pelo aprendizado —
	// vira um evento config no fluxo em tempo real.
	if h.writer != nil {
		h.writer.OnChange(h.configChanged)
	}
	h.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "recurso da API inexistente: "+r.URL.Path)
	})
	h.historyRoutes()
	h.routeRoutes()
	h.settingsRoutes()
	h.deriveRoutes()
	h.eventRoutes()
	h.upstreamRoutes()
	h.mux.Handle("/", spa(d.Web))
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// handle registra os handlers de um path por método e responde 405 aos
// demais métodos, com o cabeçalho Allow.
func (h *Handler) handle(path string, byMethod map[string]http.HandlerFunc) {
	methods := slices.Sorted(maps.Keys(byMethod))
	for _, m := range methods {
		h.mux.HandleFunc(m+" "+path, byMethod[m])
	}
	h.mux.HandleFunc(path, methodNotAllowed(strings.Join(methods, ", ")))
}

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

// apiError é o corpo de toda resposta de erro da API.
type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	// Field é o campo responsável, quando há.
	Field string `json:"field,omitempty"`
	// File é o documento responsável, quando há.
	File string `json:"file,omitempty"`
	// Line e Column localizam o campo no documento, a partir de 1.
	Line   int `json:"line,omitempty"`
	Column int `json:"column,omitempty"`
	// Env é a variável de ambiente que define o valor, quando é o caso.
	Env string `json:"env,omitempty"`
	// Errors lista os problemas quando há mais de um.
	Errors []errorDetail `json:"errors,omitempty"`
}

// errorDetail é um dos problemas de uma resposta com vários.
type errorDetail struct {
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
}

// ApplyError recusa aplicar a quente uma mudança da configuração do
// processo, como uma porta indisponível ou um backend do histórico que não
// inicializa. Code é o código de erro da API (port_unavailable,
// backend_unavailable); a resposta é 409.
type ApplyError struct {
	Code    string
	Field   string
	Message string
}

func (e *ApplyError) Error() string { return e.Message }

// requestError é uma requisição recusada antes de chegar à configuração.
type requestError struct {
	status int
	body   apiError
}

func (e *requestError) Error() string { return e.body.Message }

func badRequest(msg string) error {
	return &requestError{http.StatusBadRequest, apiError{Error: "bad_request", Message: msg}}
}

func notFound(msg string) error {
	return &requestError{http.StatusNotFound, apiError{Error: "not_found", Message: msg}}
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

// writeErr traduz o erro de uma operação na resposta da API.
func writeErr(w http.ResponseWriter, err error) {
	var re *requestError
	var ae *ApplyError
	var ces config.Errors
	var ce *config.Error
	switch {
	case errors.As(err, &re):
		writeJSON(w, re.status, re.body)
	case errors.Is(err, writer.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, writer.ErrStale):
		writeError(w, http.StatusPreconditionFailed, "stale",
			"o documento mudou desde a versão informada em If-Match; releia e tente de novo")
	case errors.As(err, &ae):
		writeJSON(w, http.StatusConflict, apiError{Error: ae.Code, Message: ae.Message, Field: ae.Field})
	case errors.As(err, &ces) && len(ces) > 0:
		writeConfigErrors(w, ces)
	case errors.As(err, &ce):
		writeConfigErrors(w, config.Errors{ce})
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

// writeConfigErrors responde a uma configuração recusada: 409 quando há
// colisão com outro documento, 422 quando um valor é inválido. Em ambos os
// casos nada foi gravado.
func writeConfigErrors(w http.ResponseWriter, es config.Errors) {
	status, code := http.StatusUnprocessableEntity, "invalid"
	if es.HasConflict() {
		status, code = http.StatusConflict, "conflict"
	}
	first := es[0]
	body := apiError{
		Error:   code,
		Message: es.Error(),
		Field:   first.Field,
		File:    first.File,
		Line:    first.Line,
		Column:  first.Column,
	}
	if len(es) > 1 {
		for _, e := range es {
			body.Errors = append(body.Errors, errorDetail{
				Message: e.Error(), Field: e.Field, File: e.File, Line: e.Line, Column: e.Column,
			})
		}
	}
	writeJSON(w, status, body)
}

// methodNotAllowed responde 405 aos métodos não suportados num path da API.
func methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			"método "+r.Method+" não suportado em "+r.URL.Path+"; use "+allow)
	}
}
