package admin

import (
	"net/http"
	"strings"

	"github.com/gamerjp64/gateway/internal/upstream"
)

func (h *Handler) upstreamRoutes() {
	h.handle("/api/upstreams", map[string]http.HandlerFunc{
		"GET": h.listUpstreams,
	})
}

// upstreamsBody é o corpo de GET /api/upstreams e do evento upstreams.
type upstreamsBody struct {
	Items []upstream.Item `json:"items"`
}

func (h *Handler) upstreamsReport() upstreamsBody {
	return upstreamsBody{Items: h.upstreams.Report(h.live.Load().Routes)}
}

// listUpstreams devolve a disponibilidade recente de cada upstream declarado.
func (h *Handler) listUpstreams(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.upstreamsReport())
}

// upstreamsKey resume o que o evento upstreams anuncia: o status de cada
// upstream. As contagens mudam a cada requisição e não geram evento; o
// painel as relê junto com o resto quando recebe um.
func upstreamsKey(b upstreamsBody) string {
	var s strings.Builder
	for _, it := range b.Items {
		s.WriteString(it.Upstream + "=" + string(it.Status) + "\n")
	}
	return s.String()
}
