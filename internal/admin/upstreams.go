package admin

import (
	"net/http"
	"strings"

	"github.com/joaovillas/devgateway/internal/upstream"
)

func (h *Handler) upstreamRoutes() {
	h.handle("/api/upstreams", map[string]http.HandlerFunc{
		"GET": h.listUpstreams,
	})
}

// upstreamsBody is the body of GET /api/upstreams and of the upstreams
// event.
type upstreamsBody struct {
	Items []upstream.Item `json:"items"`
}

func (h *Handler) upstreamsReport() upstreamsBody {
	return upstreamsBody{Items: h.upstreams.Report(h.live.Load().Routes)}
}

// listUpstreams returns the recent availability of every declared upstream.
func (h *Handler) listUpstreams(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.upstreamsReport())
}

// upstreamsKey summarizes what the upstreams event announces: the status
// of each upstream. The counters change on every request and raise no
// event; the panel re-reads them along with the rest when one arrives.
func upstreamsKey(b upstreamsBody) string {
	var s strings.Builder
	for _, it := range b.Items {
		s.WriteString(it.Upstream + "=" + string(it.Status) + "\n")
	}
	return s.String()
}
