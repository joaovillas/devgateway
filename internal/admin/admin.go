// Package admin serves the admin port: the REST API and the web UI.
// The API contract lives in docs/api.md.
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

	"github.com/gamerjp64/devgateway/internal/capture"
	"github.com/gamerjp64/devgateway/internal/config"
	"github.com/gamerjp64/devgateway/internal/config/writer"
	"github.com/gamerjp64/devgateway/internal/override"
	"github.com/gamerjp64/devgateway/internal/store"
	"github.com/gamerjp64/devgateway/internal/upstream"
)

// Deps gathers what the API operates on.
type Deps struct {
	Live *config.Live
	// Web is the embedded frontend.
	Web fs.FS
	// History is the history in use, with a hot-swappable backend.
	History *store.Switchable
	// Recorder records the exchanges; the API waits on it before reading
	// the history, and uses it to clear the history.
	Recorder *capture.Recorder
	// Writer serializes route document writes and the reload.
	Writer *writer.Writer
	// Overrides is the live state of the overrides.
	Overrides *override.Tracker
	// Upstreams is the recent availability of the upstreams, fed by the
	// traffic port. nil: a tracker of its own, always unknown.
	Upstreams *upstream.Health
	// Loader re-reads gateway.json and the routes directory on a reload.
	Loader config.Loader
	// Apply hot-applies whatever the process configuration controls outside
	// the snapshot (ports, history backend) before the new snapshot is
	// published. It prepares what is needed, calls persist (which writes
	// gateway.json on a change through the API; nil on a reload) and only
	// then swaps. An error rejects the change and keeps the configuration
	// in force; an *ApplyError picks the response code. nil: nothing to
	// apply beyond persist.
	Apply func(old, next config.Settings, persist func() error) error
	// Ports reports the ports the process is listening on right now, which
	// differ from the configured ones when the configuration asks for port
	// 0. nil: the configured ones.
	Ports func() (traffic, admin int)
	// StartedAt is the moment the process came up.
	StartedAt time.Time
	// Heartbeat is the heartbeat interval of the event stream; zero uses
	// DefaultHeartbeat.
	Heartbeat time.Duration
}

// Version is the gateway version reported by the API.
var Version = "0.1.0"

// Handler is the handler of the admin port.
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

// New builds the API over the dependencies and serves the embedded frontend.
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
	// Every applied change, whether it comes from the API, a reload or
	// learning, becomes a config event on the real-time stream.
	if h.writer != nil {
		h.writer.OnChange(h.configChanged)
	}
	h.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such API resource: "+r.URL.Path)
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

// handle registers the handlers of a path by method and answers 405 to
// every other method, with the Allow header.
func (h *Handler) handle(path string, byMethod map[string]http.HandlerFunc) {
	methods := slices.Sorted(maps.Keys(byMethod))
	for _, m := range methods {
		h.mux.HandleFunc(m+" "+path, byMethod[m])
	}
	h.mux.HandleFunc(path, methodNotAllowed(strings.Join(methods, ", ")))
}

// spa serves the embedded frontend; paths that are not files fall back to
// index.html, so that client-side navigation works.
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

// apiError is the body of every API error response.
type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	// Field is the field at fault, when there is one.
	Field string `json:"field,omitempty"`
	// File is the document at fault, when there is one.
	File string `json:"file,omitempty"`
	// Line and Column locate the field in the document, starting at 1.
	Line   int `json:"line,omitempty"`
	Column int `json:"column,omitempty"`
	// Env is the environment variable that sets the value, when that is
	// the case.
	Env string `json:"env,omitempty"`
	// Errors lists the problems when there is more than one.
	Errors []errorDetail `json:"errors,omitempty"`
}

// errorDetail is one of the problems of a response carrying several.
type errorDetail struct {
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
}

// ApplyError refuses to hot-apply a change to the process configuration,
// such as an unavailable port or a history backend that fails to start.
// Code is the API error code (port_unavailable, backend_unavailable); the
// response is 409.
type ApplyError struct {
	Code    string
	Field   string
	Message string
}

func (e *ApplyError) Error() string { return e.Message }

// requestError is a request rejected before it reaches the configuration.
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

// writeErr turns the error of an operation into the API response.
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
			"the document changed since the version given in If-Match; re-read it and try again")
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

// writeConfigErrors answers a rejected configuration: 409 when it collides
// with another document, 422 when a value is invalid. In both cases
// nothing was written.
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

// methodNotAllowed answers 405 to the methods an API path does not support.
func methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			"method "+r.Method+" is not supported on "+r.URL.Path+"; use "+allow)
	}
}
