package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gamerjp64/devgateway/internal/config"
	"github.com/gamerjp64/devgateway/internal/exchange"
	"github.com/gamerjp64/devgateway/internal/override"
	"github.com/gamerjp64/devgateway/internal/store"
)

// deriveRoutes registers the POST only: the other methods on .../derive
// fall through to the override resource named "derive", which stays
// reachable.
func (h *Handler) deriveRoutes() {
	h.mux.HandleFunc("POST /api/routes/{route}/overrides/derive", h.deriveOverride)
}

// deriveRequest asks for an override built from an exchange in the history.
type deriveRequest struct {
	// Exchange is the identifier of the source exchange.
	Exchange string `json:"exchange"`
	// Name is the override name; empty derives it from the method and the
	// path.
	Name string `json:"name,omitempty"`
	// Save writes the override instead of only returning the draft.
	Save bool `json:"save,omitempty"`
}

// deriveDraft is the draft of a derived override, for review before it
// takes effect: nothing was written.
type deriveDraft struct {
	Route    string          `json:"route"`
	Override config.Override `json:"override"`
	Warnings []string        `json:"warnings"`
}

// derivedResource is the derived override, once written, with the
// warnings from the derivation.
type derivedResource struct {
	overrideResource
	Warnings []string `json:"warnings"`
}

// deriveOverride builds an override from an exchange in the history: the
// exact path and method of the observed request as the criteria, and the
// upstream response (status, headers and body) as the declared response.
// By default it returns the draft only, without writing: that is the
// review step, after which the override (edited or not) is created by the
// creation POST. With save, it writes straight away. An exchange whose
// response body was truncated on capture is not rejected: the override
// comes out with source.bodyIncomplete and a warning.
func (h *Handler) deriveOverride(w http.ResponseWriter, r *http.Request) {
	var req deriveRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.Exchange == "" {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Field: "exchange",
			Message: "exchange is required: the identifier of the source exchange"})
		return
	}
	s := h.live.Load().Settings
	if !s.HistoryExpose {
		writeJSON(w, http.StatusForbidden, historyDisabled(s))
		return
	}
	_, route, err := h.lookup(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), syncTimeout)
	h.rec.Sync(ctx)
	cancel()
	e, err := h.history.Get(r.Context(), req.Exchange)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, notFound(fmt.Sprintf("exchange %s not found in the history", req.Exchange)))
		return
	}
	if err != nil {
		writeStoreError(w, err, "")
		return
	}
	if msg := underivable(e); msg != "" {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Field: "exchange", Message: msg})
		return
	}

	o := override.FromExchange(e, config.SourceDerived, h.now())
	warnings := []string{}
	if e.Response.Truncated {
		warnings = append(warnings, fmt.Sprintf(
			"the response body was truncated on capture (%d of %d bytes); the override replays only the captured part",
			len(e.Response.Body), e.Response.Size))
	}
	if e.Error != "" {
		o.Source.BodyIncomplete = true
		warnings = append(warnings, "the response transfer was interrupted ("+e.Error+"); the captured body is incomplete")
	}
	o.Name = req.Name
	if o.Name == "" {
		o.Name = override.Name(route.Doc, e.Method, e.Path)
	} else if route.Override(o.Name) != nil && !req.Save {
		warnings = append(warnings, fmt.Sprintf("route %q already has an override named %q; pick another name before creating it", route.Name(), o.Name))
	}
	if override.Known(route.Doc, e.Method, e.Path) {
		warnings = append(warnings, fmt.Sprintf("route %q already has an override for %s %s", route.Name(), e.Method, e.Path))
	}
	if e.Route != route.Name() {
		warnings = append(warnings, fmt.Sprintf("the exchange was served by route %q, not by %q", e.Route, route.Name()))
	}
	if err := config.ValidateOverride(route.File, o); err != nil {
		writeErr(w, err)
		return
	}
	if !req.Save {
		writeJSON(w, http.StatusOK, deriveDraft{Route: route.Name(), Override: o, Warnings: warnings})
		return
	}

	res, err := h.updateOverrides(r, func(file string, list []config.Override) ([]config.Override, error) {
		if indexOf(list, o.Name) >= 0 {
			return nil, overrideConflict(file, route.Name(), o.Name)
		}
		return append(list, o), nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	saved := res.Snapshot.Route(res.Route)
	var out overrideResource
	ok := saved != nil
	if ok {
		out, ok = h.overrideResource(saved, o.Name)
	}
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "the override that was written is not in the configuration in force")
		return
	}
	setETag(w, res.Version)
	w.Header().Set("Location", "/api/routes/"+url.PathEscape(res.Route)+"/overrides/"+url.PathEscape(o.Name))
	writeJSON(w, http.StatusCreated, derivedResource{overrideResource: out, Warnings: warnings})
}

// underivable explains why the exchange cannot yield an override, or
// returns empty when it can: only a response from the upstream itself can
// be replayed.
func underivable(e exchange.Exchange) string {
	switch {
	case e.Outcome == exchange.OutcomeSynthesized:
		return fmt.Sprintf("exchange %s was answered by override %s, not by the upstream; derive from an exchange the upstream answered", e.ID, e.Override)
	case e.Outcome == exchange.OutcomeDropped:
		return fmt.Sprintf("exchange %s had its connection dropped by override %s and has no response to replay", e.ID, e.Override)
	case e.Outcome == exchange.OutcomeGateway:
		return fmt.Sprintf("exchange %s has no upstream response: the gateway itself answered (%s)", e.ID, e.Error)
	case e.Status == 0:
		return fmt.Sprintf("exchange %s never got a response (%s)", e.ID, e.Error)
	case e.Status == http.StatusSwitchingProtocols:
		return fmt.Sprintf("exchange %s is a protocol upgrade, which an override cannot replay", e.ID)
	}
	return ""
}
