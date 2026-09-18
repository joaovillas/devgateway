package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"time"

	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/config/writer"
	"github.com/gamerjp64/gateway/internal/override"
)

func (h *Handler) routeRoutes() {
	h.handle("/api/routes", map[string]http.HandlerFunc{
		"GET":  h.listRoutes,
		"POST": h.createRoute,
	})
	h.handle("/api/routes/{route}", map[string]http.HandlerFunc{
		"GET":    h.getRoute,
		"PUT":    h.replaceRoute,
		"PATCH":  h.patchRoute,
		"DELETE": h.deleteRoute,
	})
	h.handle("/api/routes/{route}/document", map[string]http.HandlerFunc{
		"GET": h.getDocument,
		"PUT": h.putDocument,
	})
	h.handle("/api/routes/{route}/overrides", map[string]http.HandlerFunc{
		"GET":  h.listOverrides,
		"POST": h.createOverride,
	})
	h.handle("/api/routes/{route}/overrides/{override}", map[string]http.HandlerFunc{
		"GET":    h.getOverride,
		"PUT":    h.replaceOverride,
		"PATCH":  h.patchOverride,
		"DELETE": h.deleteOverride,
	})
	h.handle("/api/routes/{route}/overrides/{override}/reset", map[string]http.HandlerFunc{
		"POST": h.resetOverride,
	})
	h.handle("/api/overrides/state", map[string]http.HandlerFunc{
		"GET": h.overridesState,
	})
}

// liveState é o estado vivo de um override dentro do recurso rota ou
// override, onde a rota e o nome já estão implícitos.
type liveState struct {
	Active          bool       `json:"active"`
	Expired         *string    `json:"expired"`
	RegisteredAt    time.Time  `json:"registeredAt"`
	TTLRemainingMs  *int64     `json:"ttlRemainingMs"`
	Applications    int64      `json:"applications"`
	MaxApplications *int       `json:"maxApplications"`
	LastAppliedAt   *time.Time `json:"lastAppliedAt"`
}

func nested(s override.LiveState) *liveState {
	return &liveState{
		Active:          s.Active,
		Expired:         s.Expired,
		RegisteredAt:    s.RegisteredAt,
		TTLRemainingMs:  s.TTLRemainingMs,
		Applications:    s.Applications,
		MaxApplications: s.MaxApplications,
		LastAppliedAt:   s.LastAppliedAt,
	}
}

// routeResource embrulha o documento da rota com o que não pertence a ele.
type routeResource struct {
	File string `json:"file"`
	// HasComments avisa que o documento tem comentários, que uma escrita
	// pelos campos perderia.
	HasComments bool `json:"hasComments"`
	// Order é a posição na precedência; 0 é a mais específica.
	Order int                   `json:"order"`
	Route config.Route          `json:"route"`
	State map[string]*liveState `json:"state"`
}

// overrideResource é um override com sua posição e estado vivo.
type overrideResource struct {
	Route string `json:"route"`
	// Order é a posição na precedência dentro da rota.
	Order    int             `json:"order"`
	Override config.Override `json:"override"`
	State    *liveState      `json:"state"`
}

// resource monta o recurso da rota r do snapshot, lendo o documento em disco
// para os metadados. Devolve também a versão do documento.
func (h *Handler) resource(snap *config.Snapshot, r *config.CompiledRoute) (routeResource, string) {
	res := routeResource{
		File:  r.File,
		Order: slices.Index(snap.Routes, r),
		Route: r.Doc,
		State: map[string]*liveState{},
	}
	var version string
	if data, err := h.writer.ReadDocument(r.File); err == nil {
		res.HasComments = config.HasComments(data)
		version = writer.Version(data)
	}
	for _, o := range r.Doc.Overrides {
		if s, ok := h.overrides.State(r.Name(), o.Name); ok {
			res.State[o.Name] = nested(s)
		}
	}
	return res, version
}

func (h *Handler) overrideResource(r *config.CompiledRoute, name string) (overrideResource, bool) {
	i := slices.IndexFunc(r.Overrides, func(o *config.CompiledOverride) bool { return o.Doc.Name == name })
	if i < 0 {
		return overrideResource{}, false
	}
	res := overrideResource{Route: r.Name(), Order: i, Override: r.Overrides[i].Doc}
	if s, ok := h.overrides.State(r.Name(), name); ok {
		res.State = nested(s)
	}
	return res, true
}

// lookup devolve a rota do path na configuração em vigor.
func (h *Handler) lookup(r *http.Request) (*config.Snapshot, *config.CompiledRoute, error) {
	snap := h.live.Load()
	name := r.PathValue("route")
	route := snap.Route(name)
	if route == nil {
		return nil, nil, notFound(fmt.Sprintf("rota %q não encontrada", name))
	}
	return snap, route, nil
}

func (h *Handler) listRoutes(w http.ResponseWriter, _ *http.Request) {
	snap := h.live.Load()
	items := make([]routeResource, 0, len(snap.Routes))
	for _, r := range snap.Routes {
		res, _ := h.resource(snap, r)
		items = append(items, res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) getRoute(w http.ResponseWriter, r *http.Request) {
	snap, route, err := h.lookup(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	res, version := h.resource(snap, route)
	setETag(w, version)
	writeJSON(w, http.StatusOK, res)
}

func setETag(w http.ResponseWriter, v string) {
	if v != "" {
		w.Header().Set("ETag", v)
	}
}

// written responde a uma escrita de rota com o recurso resultante.
func (h *Handler) written(w http.ResponseWriter, res writer.Result, status int) {
	route := res.Snapshot.Route(res.Route)
	if route == nil {
		writeError(w, http.StatusInternalServerError, "internal", "a rota gravada não está na configuração em vigor")
		return
	}
	out, _ := h.resource(res.Snapshot, route)
	setETag(w, res.Version)
	if status == http.StatusCreated {
		w.Header().Set("Location", "/api/routes/"+url.PathEscape(res.Route))
	}
	writeJSON(w, status, out)
}

// decodeRoute lê uma rota completa do corpo. schemaVersion ausente assume a
// versão do binário.
func decodeRoute(r *http.Request) (config.Route, error) {
	var route config.Route
	if err := decodeJSON(r, &route); err != nil {
		return route, err
	}
	if route.SchemaVersion == 0 {
		route.SchemaVersion = config.SchemaVersion
	}
	return route, nil
}

func (h *Handler) createRoute(w http.ResponseWriter, r *http.Request) {
	route, err := decodeRoute(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.writer.CreateRoute(route, writer.CauseAPI)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.written(w, res, http.StatusCreated)
}

func (h *Handler) replaceRoute(w http.ResponseWriter, r *http.Request) {
	route, err := decodeRoute(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.writer.Update(writer.RouteUpdate{
		Route:   r.PathValue("route"),
		IfMatch: r.Header.Get("If-Match"),
		Cause:   writer.CauseAPI,
		Apply: func(doc *config.Route) (bool, error) {
			if reflect.DeepEqual(*doc, route) {
				return false, nil
			}
			*doc = route
			return true, nil
		},
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	h.written(w, res, http.StatusOK)
}

func (h *Handler) patchRoute(w http.ResponseWriter, r *http.Request) {
	patch, err := readPatch(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, ok := patch["overrides"]; ok {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{
			Error:   "invalid",
			Field:   "overrides",
			Message: "overrides não é alterado por aqui; use /api/routes/{rota}/overrides",
		})
		return
	}
	res, err := h.writer.Update(writer.RouteUpdate{
		Route:   r.PathValue("route"),
		IfMatch: r.Header.Get("If-Match"),
		Cause:   writer.CauseAPI,
		Apply: func(doc *config.Route) (bool, error) {
			var next config.Route
			if err := applyPatch(*doc, patch, &next); err != nil {
				return false, err
			}
			if reflect.DeepEqual(*doc, next) {
				return false, nil
			}
			*doc = next
			return true, nil
		},
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	h.written(w, res, http.StatusOK)
}

func (h *Handler) deleteRoute(w http.ResponseWriter, r *http.Request) {
	if _, err := h.writer.DeleteRoute(r.PathValue("route"), r.Header.Get("If-Match"), writer.CauseAPI); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getDocument devolve o documento exatamente como está em disco.
func (h *Handler) getDocument(w http.ResponseWriter, r *http.Request) {
	_, route, err := h.lookup(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	data, err := h.writer.ReadDocument(route.File)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lendo "+route.File+": "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	setETag(w, writer.Version(data))
	w.Write(data)
}

// putDocument grava o documento bruto como enviado, comentários incluídos.
func (h *Handler) putDocument(w http.ResponseWriter, r *http.Request) {
	data, err := readBody(r, yamlTypes)
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.writer.WriteDocument(writer.DocumentWrite{
		Route:   r.PathValue("route"),
		Data:    data,
		IfMatch: r.Header.Get("If-Match"),
		Cause:   writer.CauseAPI,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	h.written(w, res, status)
}

func (h *Handler) listOverrides(w http.ResponseWriter, r *http.Request) {
	_, route, err := h.lookup(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]overrideResource, 0, len(route.Doc.Overrides))
	for _, o := range route.Doc.Overrides {
		if res, ok := h.overrideResource(route, o.Name); ok {
			items = append(items, res)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) getOverride(w http.ResponseWriter, r *http.Request) {
	snap, route, err := h.lookup(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	name := r.PathValue("override")
	res, ok := h.overrideResource(route, name)
	if !ok {
		writeErr(w, overrideNotFound(route.Name(), name))
		return
	}
	_, version := h.resource(snap, route)
	setETag(w, version)
	writeJSON(w, http.StatusOK, res)
}

func overrideNotFound(route, name string) error {
	return notFound(fmt.Sprintf("override %q não encontrado na rota %q", name, route))
}

// overrideConflict recusa um nome de override já usado na rota.
func overrideConflict(file, route, name string) error {
	return config.Errors{{File: file, Field: "name", Conflict: true,
		Msg: fmt.Sprintf("override %q já existe na rota %q", name, route)}}
}

// writtenOverride responde a uma escrita de override com o recurso
// resultante.
func (h *Handler) writtenOverride(w http.ResponseWriter, res writer.Result, name string, status int) {
	route := res.Snapshot.Route(res.Route)
	var out overrideResource
	ok := route != nil
	if ok {
		out, ok = h.overrideResource(route, name)
	}
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "o override gravado não está na configuração em vigor")
		return
	}
	setETag(w, res.Version)
	if status == http.StatusCreated {
		w.Header().Set("Location", "/api/routes/"+url.PathEscape(res.Route)+"/overrides/"+url.PathEscape(name))
	}
	writeJSON(w, status, out)
}

// updateOverrides altera a lista de overrides da rota do path sob o mutex de
// escrita. edit recebe a lista lida do disco e devolve a nova, ou nil para
// não gravar.
func (h *Handler) updateOverrides(r *http.Request, edit func(file string, list []config.Override) ([]config.Override, error)) (writer.Result, error) {
	_, route, err := h.lookup(r)
	if err != nil {
		return writer.Result{}, err
	}
	return h.writer.Update(writer.RouteUpdate{
		Route:   route.Name(),
		IfMatch: r.Header.Get("If-Match"),
		Cause:   writer.CauseAPI,
		Apply: func(doc *config.Route) (bool, error) {
			next, err := edit(route.File, slices.Clone(doc.Overrides))
			if err != nil || next == nil {
				return false, err
			}
			if reflect.DeepEqual(doc.Overrides, next) {
				return false, nil
			}
			doc.Overrides = next
			if len(doc.Overrides) == 0 {
				doc.Overrides = nil
			}
			return true, nil
		},
	})
}

func indexOf(list []config.Override, name string) int {
	return slices.IndexFunc(list, func(o config.Override) bool { return o.Name == name })
}

func (h *Handler) createOverride(w http.ResponseWriter, r *http.Request) {
	var o config.Override
	if err := decodeJSON(r, &o); err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.updateOverrides(r, func(file string, list []config.Override) ([]config.Override, error) {
		if indexOf(list, o.Name) >= 0 {
			return nil, overrideConflict(file, r.PathValue("route"), o.Name)
		}
		return append(list, o), nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	h.writtenOverride(w, res, o.Name, http.StatusCreated)
}

// replaceAt substitui o override de nome dado por next, na mesma posição,
// recusando um nome novo já usado por outro override da rota.
func replaceAt(file, route, name string, list []config.Override, next func(config.Override) (config.Override, error)) ([]config.Override, error) {
	i := indexOf(list, name)
	if i < 0 {
		return nil, overrideNotFound(route, name)
	}
	o, err := next(list[i])
	if err != nil {
		return nil, err
	}
	if o.Name != name && indexOf(list, o.Name) >= 0 {
		return nil, overrideConflict(file, route, o.Name)
	}
	list[i] = o
	return list, nil
}

func (h *Handler) replaceOverride(w http.ResponseWriter, r *http.Request) {
	var o config.Override
	if err := decodeJSON(r, &o); err != nil {
		writeErr(w, err)
		return
	}
	name := r.PathValue("override")
	res, err := h.updateOverrides(r, func(file string, list []config.Override) ([]config.Override, error) {
		return replaceAt(file, r.PathValue("route"), name, list, func(config.Override) (config.Override, error) { return o, nil })
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	h.writtenOverride(w, res, o.Name, http.StatusOK)
}

// patchOverride aplica um JSON Merge Patch ao override. É o caminho dos
// controles contínuos e do liga/desliga ({"enabled": false}); a API não liga
// o override por conta própria.
func (h *Handler) patchOverride(w http.ResponseWriter, r *http.Request) {
	patch, err := readPatch(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	name := r.PathValue("override")
	final := name
	res, err := h.updateOverrides(r, func(file string, list []config.Override) ([]config.Override, error) {
		return replaceAt(file, r.PathValue("route"), name, list, func(cur config.Override) (config.Override, error) {
			var next config.Override
			err := applyPatch(cur, patch, &next)
			final = next.Name
			return next, err
		})
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	h.writtenOverride(w, res, final, http.StatusOK)
}

func (h *Handler) deleteOverride(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("override")
	_, err := h.updateOverrides(r, func(_ string, list []config.Override) ([]config.Override, error) {
		i := indexOf(list, name)
		if i < 0 {
			return nil, overrideNotFound(r.PathValue("route"), name)
		}
		return slices.Delete(list, i, i+1), nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resetOverride recomeça o tempo de vida e a contagem de aplicações sem
// alterar o documento.
func (h *Handler) resetOverride(w http.ResponseWriter, r *http.Request) {
	_, route, err := h.lookup(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	name := r.PathValue("override")
	if route.Override(name) == nil || !h.overrides.Reset(route.Name(), name) {
		writeErr(w, overrideNotFound(route.Name(), name))
		return
	}
	res, _ := h.overrideResource(route, name)
	writeJSON(w, http.StatusOK, res)
}

func (h *Handler) overridesState(w http.ResponseWriter, _ *http.Request) {
	items, now := h.overrides.States()
	writeJSON(w, http.StatusOK, map[string]any{"now": now, "items": items})
}
