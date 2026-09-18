package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/exchange"
	"github.com/gamerjp64/gateway/internal/override"
	"github.com/gamerjp64/gateway/internal/store"
)

// deriveRoutes registra só o POST: os demais métodos em .../derive caem no
// recurso override de nome "derive", que continua acessível.
func (h *Handler) deriveRoutes() {
	h.mux.HandleFunc("POST /api/routes/{route}/overrides/derive", h.deriveOverride)
}

// deriveRequest pede um override montado a partir de uma troca do histórico.
type deriveRequest struct {
	// Exchange é o identificador da troca de origem.
	Exchange string `json:"exchange"`
	// Name é o nome do override; vazio deriva do método e do path.
	Name string `json:"name,omitempty"`
	// Save grava o override em vez de apenas devolver o rascunho.
	Save bool `json:"save,omitempty"`
}

// deriveDraft é o rascunho de um override derivado, para revisão antes de
// valer: nada foi gravado.
type deriveDraft struct {
	Route    string          `json:"route"`
	Override config.Override `json:"override"`
	Warnings []string        `json:"warnings"`
}

// derivedResource é o override derivado e gravado, com os avisos da
// derivação.
type derivedResource struct {
	overrideResource
	Warnings []string `json:"warnings"`
}

// deriveOverride monta um override a partir de uma troca do histórico: path
// exato e método da requisição observada como critério, e a resposta do
// upstream — status, cabeçalhos e corpo — como resposta declarada. Por
// padrão devolve só o rascunho, sem gravar: é a etapa de revisão, depois da
// qual o override (editado ou não) é criado pelo POST de criação. Com save,
// grava direto. Uma troca com o corpo da resposta truncado na captura não é
// recusada: o override sai com source.bodyIncomplete e um aviso.
func (h *Handler) deriveOverride(w http.ResponseWriter, r *http.Request) {
	var req deriveRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.Exchange == "" {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Field: "exchange",
			Message: "exchange é obrigatório: o identificador da troca de origem"})
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
		writeErr(w, notFound(fmt.Sprintf("troca %s não encontrada no histórico", req.Exchange)))
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
			"o corpo da resposta foi truncado na captura (%d de %d bytes); o override reproduz só a parte capturada",
			len(e.Response.Body), e.Response.Size))
	}
	if e.Error != "" {
		o.Source.BodyIncomplete = true
		warnings = append(warnings, "a transferência da resposta foi interrompida ("+e.Error+"); o corpo capturado está incompleto")
	}
	o.Name = req.Name
	if o.Name == "" {
		o.Name = override.Name(route.Doc, e.Method, e.Path)
	} else if route.Override(o.Name) != nil && !req.Save {
		warnings = append(warnings, fmt.Sprintf("a rota %q já tem um override chamado %q; escolha outro nome antes de criar", route.Name(), o.Name))
	}
	if override.Known(route.Doc, e.Method, e.Path) {
		warnings = append(warnings, fmt.Sprintf("a rota %q já tem um override para %s %s", route.Name(), e.Method, e.Path))
	}
	if e.Route != route.Name() {
		warnings = append(warnings, fmt.Sprintf("a troca foi atendida pela rota %q, não por %q", e.Route, route.Name()))
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
		writeError(w, http.StatusInternalServerError, "internal", "o override gravado não está na configuração em vigor")
		return
	}
	setETag(w, res.Version)
	w.Header().Set("Location", "/api/routes/"+url.PathEscape(res.Route)+"/overrides/"+url.PathEscape(o.Name))
	writeJSON(w, http.StatusCreated, derivedResource{overrideResource: out, Warnings: warnings})
}

// underivable explica por que a troca não pode originar um override, ou
// devolve vazio quando pode: só uma resposta do próprio upstream é
// reproduzível.
func underivable(e exchange.Exchange) string {
	switch {
	case e.Outcome == exchange.OutcomeSynthesized:
		return fmt.Sprintf("a troca %s foi respondida pelo override %s, não pelo upstream; derive de uma troca respondida pelo upstream", e.ID, e.Override)
	case e.Outcome == exchange.OutcomeDropped:
		return fmt.Sprintf("a troca %s teve a conexão derrubada pelo override %s e não tem resposta a reproduzir", e.ID, e.Override)
	case e.Outcome == exchange.OutcomeGateway:
		return fmt.Sprintf("a troca %s não tem resposta do upstream: o próprio gateway respondeu (%s)", e.ID, e.Error)
	case e.Status == 0:
		return fmt.Sprintf("a troca %s não chegou a ter resposta (%s)", e.ID, e.Error)
	case e.Status == http.StatusSwitchingProtocols:
		return fmt.Sprintf("a troca %s é um upgrade de protocolo, que um override não reproduz", e.ID)
	}
	return ""
}
