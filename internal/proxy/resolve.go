package proxy

import (
	"net"
	"net/http"
	"strings"

	"github.com/gamerjp64/gateway/internal/config"
)

// Resolve devolve a rota que atende a requisição, ou nil. As rotas do
// snapshot já estão em ordem de precedência (host antes de path, path mais
// específico antes do menos específico), então vale a primeira que casa.
func Resolve(snap *config.Snapshot, r *http.Request) *config.CompiledRoute {
	host := requestHost(r)
	for _, rt := range snap.Routes {
		if rt.Doc.Match.Host != "" && !hostMatches(rt.Doc.Match.Host, host) {
			continue
		}
		if rt.Path != nil && !rt.Path.Match(r.URL.Path) {
			continue
		}
		return rt
	}
	return nil
}

func requestHost(r *http.Request) string {
	return strings.ToLower(r.Host)
}

// hostMatches aceita o host declarado com ou sem porta: "payments.local"
// casa com "payments.local:8080"; "payments.local:8080" exige a porta.
func hostMatches(declared, host string) bool {
	declared = strings.ToLower(declared)
	if declared == host {
		return true
	}
	if strings.Contains(declared, ":") {
		return false
	}
	h, _, err := net.SplitHostPort(host)
	return err == nil && h == declared
}
