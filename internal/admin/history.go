package admin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/joaovillas/devgateway/internal/config"
	"github.com/joaovillas/devgateway/internal/exchange"
	"github.com/joaovillas/devgateway/internal/store"
)

// syncTimeout caps the wait for the already finished exchanges to be
// written before a read of the history. Past the limit, the read goes on
// with whatever is written by then.
const syncTimeout = 2 * time.Second

func (h *Handler) historyRoutes() {
	h.mux.HandleFunc("GET /api/exchanges", h.exposed(h.listExchanges))
	h.mux.HandleFunc("DELETE /api/exchanges", h.clearExchanges)
	h.mux.HandleFunc("/api/exchanges", methodNotAllowed("GET, DELETE"))
	h.mux.HandleFunc("GET /api/exchanges/{id}", h.exposed(h.getExchange))
	h.mux.HandleFunc("/api/exchanges/{id}", methodNotAllowed("GET"))
	h.mux.HandleFunc("GET /api/exchanges/{id}/older", h.exposed(h.neighbor(store.Older)))
	h.mux.HandleFunc("/api/exchanges/{id}/older", methodNotAllowed("GET"))
	h.mux.HandleFunc("GET /api/exchanges/{id}/newer", h.exposed(h.neighbor(store.Newer)))
	h.mux.HandleFunc("/api/exchanges/{id}/newer", methodNotAllowed("GET"))
}

// exposed refuses to read the history with history.expose off, with a
// response distinct from an empty list. Before reading, it waits for the
// exchanges that were already answered to be written, so that whoever has
// just made a request finds it.
func (h *Handler) exposed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := h.live.Load().Settings
		if !s.HistoryExpose {
			writeJSON(w, http.StatusForbidden, historyDisabled(s))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), syncTimeout)
		h.rec.Sync(ctx)
		cancel()
		next(w, r)
	}
}

func historyDisabled(s config.Settings) apiError {
	e := apiError{Error: "history_disabled", Field: "history.expose"}
	src := s.Sources["history.expose"]
	origin := "built-in default"
	switch src.Origin {
	case config.OriginEnv:
		e.Env = src.Name
		origin = "environment variable " + src.Name
	case config.OriginFile:
		e.File = src.Name
		origin = "file " + src.Name
	}
	e.Message = "the history is disabled: history.expose = false (origin: " + origin + ")"
	return e
}

// listResponse is one page of the history listing.
type listResponse struct {
	Items []exchange.Exchange `json:"items"`
	Next  string              `json:"next"`
	// Recording says whether recording is on; when it is off, that alone
	// can be why the list is empty.
	Recording bool   `json:"recording"`
	Backend   string `json:"backend"`
}

func (h *Handler) listExchanges(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, err := parseFilter(q)
	if err != nil {
		writeBadParam(w, err)
		return
	}
	var p store.Page
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeBadParam(w, &paramError{"limit", "must be a positive integer"})
			return
		}
		p.Limit = n
	}
	p.Cursor = q.Get("cursor")
	res, err := h.history.List(r.Context(), f, p)
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	items := make([]exchange.Exchange, len(res.Items))
	for i, e := range res.Items {
		items[i] = e.Summary()
	}
	writeJSON(w, http.StatusOK, listResponse{
		Items:     items,
		Next:      res.Next,
		Recording: h.live.Load().Settings.HistoryRecord,
		Backend:   h.history.Backend(),
	})
}

func (h *Handler) getExchange(w http.ResponseWriter, r *http.Request) {
	e, err := h.history.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (h *Handler) neighbor(d store.Direction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, err := parseFilter(r.URL.Query())
		if err != nil {
			writeBadParam(w, err)
			return
		}
		e, err := h.history.Neighbor(r.Context(), r.PathValue("id"), d, f)
		if err != nil {
			noMore := "no older exchange matches this filter"
			if d == store.Newer {
				noMore = "no newer exchange matches this filter"
			}
			writeStoreError(w, err, noMore)
			return
		}
		writeJSON(w, http.StatusOK, e)
	}
}

// clearExchanges empties the history in use. It works with exposure off
// too, because it returns no data.
func (h *Handler) clearExchanges(w http.ResponseWriter, r *http.Request) {
	if err := h.rec.Clear(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to clear the history: "+err.Error())
		return
	}
	h.events.publish("history", historyEvent{Cause: "cleared", Backend: h.history.Backend()})
	w.WriteHeader(http.StatusNoContent)
}

func writeStoreError(w http.ResponseWriter, err error, noMore string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, store.ErrNoMore):
		writeError(w, http.StatusNotFound, "no_more", noMore)
	case errors.Is(err, store.ErrBadCursor):
		writeJSON(w, http.StatusBadRequest, apiError{Error: "bad_cursor", Message: err.Error(), Field: "cursor"})
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

// paramError is an invalid query parameter.
type paramError struct{ field, msg string }

func (e *paramError) Error() string { return "query parameter " + e.field + ": " + e.msg }

func writeBadParam(w http.ResponseWriter, err error) {
	pe := err.(*paramError)
	writeJSON(w, http.StatusBadRequest, apiError{Error: "bad_request", Message: pe.Error(), Field: pe.field})
}

// parseFilter reads the filters from the query string, mirroring
// exchange.Filter.
func parseFilter(q url.Values) (exchange.Filter, error) {
	f := exchange.Filter{
		Route:    q.Get("route"),
		Upstream: q.Get("upstream"),
		Override: q.Get("override"),
		Method:   q.Get("method"),
		Path:     q.Get("path"),
	}
	for _, p := range []struct {
		key string
		dst *int
	}{{"statusMin", &f.StatusMin}, {"statusMax", &f.StatusMax}} {
		v := q.Get(p.key)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return f, &paramError{p.key, "must be an integer HTTP status"}
		}
		*p.dst = n
	}
	if v := q.Get("intervened"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return f, &paramError{"intervened", "must be true or false"}
		}
		f.Intervened = &b
	}
	for _, p := range []struct {
		key string
		dst *time.Time
	}{{"since", &f.Since}, {"until", &f.Until}} {
		v := q.Get(p.key)
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return f, &paramError{p.key, "must be an RFC 3339 instant, such as 2026-09-18T15:04:05Z"}
		}
		*p.dst = t
	}
	return f, nil
}
