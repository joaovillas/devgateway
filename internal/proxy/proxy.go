// Package proxy atende a porta de tráfego: resolve a rota e encaminha ao
// upstream sobre httputil.ReverseProxy.
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"sync/atomic"
	"time"

	"github.com/gamerjp64/gateway/internal/config"
)

// DefaultTimeout vale para rotas que não declaram timeout.
const DefaultTimeout = 30 * time.Second

// Handler é o handler da porta de tráfego.
type Handler struct {
	live      *config.Live
	transport http.RoundTripper
}

func NewHandler(live *config.Live) *Handler {
	return &Handler{live: live, transport: newTransport()}
}

func newTransport() *http.Transport {
	return &http.Transport{
		Proxy: nil, // o gateway fala direto com o upstream, sem proxy do ambiente
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// O snapshot é capturado uma única vez: uma recarga no meio da requisição
	// não a afeta.
	snap := h.live.Load()
	route := Resolve(snap, r)
	if route == nil {
		writeNoRoute(w, snap)
		return
	}
	if route.Upstream == nil {
		writeDiag(w, http.StatusNotImplemented, diag{
			Error:   "no_upstream",
			Message: "a rota não declara upstream e nenhum override interceptou a requisição",
			Route:   route.Name(),
		})
		return
	}
	h.forward(w, r, route)
}

// forward encaminha ao upstream. O tempo limite da rota vale até a chegada
// dos cabeçalhos da resposta: depois disso o corpo pode ser um stream longo.
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, route *config.CompiledRoute) {
	timeout := DefaultTimeout
	if route.Doc.Timeout != nil {
		timeout = time.Duration(*route.Doc.Timeout)
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var timedOut atomic.Bool
	timer := time.AfterFunc(timeout, func() {
		timedOut.Store(true)
		cancel()
	})
	defer timer.Stop()

	rp := &httputil.ReverseProxy{
		Transport: h.transport,
		Rewrite:   rewriteFor(route),
		ModifyResponse: func(*http.Response) error {
			timer.Stop()
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			switch {
			case timedOut.Load():
				writeDiag(w, http.StatusGatewayTimeout, diag{
					Error:    "upstream_timeout",
					Message:  "o upstream excedeu o tempo limite de " + timeout.String(),
					Route:    route.Name(),
					Upstream: route.Doc.Upstream,
				})
			case errors.Is(err, context.Canceled):
				// O cliente desistiu; não há a quem responder.
			default:
				writeDiag(w, http.StatusBadGateway, diag{
					Error:    "upstream_unavailable",
					Message:  "não foi possível obter resposta do upstream: " + err.Error(),
					Route:    route.Name(),
					Upstream: route.Doc.Upstream,
				})
			}
		},
	}
	rp.ServeHTTP(w, r.WithContext(ctx))
}

func rewriteFor(route *config.CompiledRoute) func(*httputil.ProxyRequest) {
	return func(pr *httputil.ProxyRequest) {
		if route.Doc.StripPrefix {
			u := pr.Out.URL
			u.Path = route.Path.Strip(u.Path)
			if u.RawPath != "" {
				u.RawPath = route.Path.Strip(u.RawPath)
			}
		}
		pr.SetURL(route.Upstream)
		// Rewrite descarta o X-Forwarded-For de entrada; recolocá-lo antes de
		// SetXForwarded faz o endereço do cliente ser acrescentado à cadeia.
		pr.Out.Header["X-Forwarded-For"] = pr.In.Header["X-Forwarded-For"]
		pr.SetXForwarded()
		if route.Doc.PreserveHost {
			pr.Out.Host = pr.In.Host
		}
	}
}

// diag é o corpo das respostas produzidas pelo próprio gateway.
type diag struct {
	Error    string   `json:"error"`
	Message  string   `json:"message"`
	Route    string   `json:"route,omitempty"`
	Upstream string   `json:"upstream,omitempty"`
	Patterns []string `json:"patterns,omitempty"`
}

func writeDiag(w http.ResponseWriter, status int, d diag) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(d)
}

func writeNoRoute(w http.ResponseWriter, snap *config.Snapshot) {
	patterns := make([]string, 0, len(snap.Routes))
	for _, r := range snap.Routes {
		patterns = append(patterns, r.Pattern())
	}
	writeDiag(w, http.StatusNotFound, diag{
		Error:    "no_route",
		Message:  "nenhuma rota casou com a requisição",
		Patterns: patterns,
	})
}
