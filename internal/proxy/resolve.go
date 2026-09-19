package proxy

import (
	"net"
	"net/http"
	"strings"

	"github.com/joaovillas/devgateway/internal/config"
)

// Resolve returns the route that serves the request, or nil. The snapshot's
// routes are already in precedence order (host before path, more specific
// path before less specific), so the first one that matches wins.
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

// hostMatches accepts the declared host with or without a port:
// "payments.local" matches "payments.local:8080"; "payments.local:8080"
// requires the port.
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
