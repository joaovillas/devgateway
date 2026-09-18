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
	"slices"
	"strings"
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
		// Sem isto o Transport pediria gzip ao upstream por conta própria e
		// descompactaria a resposta, acrescentando um Accept-Encoding que o
		// cliente não enviou e alterando o corpo que ele recebe.
		DisableCompression: true,
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
		writeDiag(w, http.StatusNotImplemented, Ident{Route: route.Name()}, diag{
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

	id := Ident{Route: route.Name()}
	rp := &httputil.ReverseProxy{
		Transport: h.transport,
		Rewrite:   rewriteFor(route, id),
		ModifyResponse: func(res *http.Response) error {
			timer.Stop()
			res.Header.Set(HeaderGateway, id.String())
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			switch {
			case timedOut.Load():
				writeDiag(w, http.StatusGatewayTimeout, id, diag{
					Error:    "upstream_timeout",
					Message:  "o upstream excedeu o tempo limite de " + timeout.String(),
					Route:    route.Name(),
					Upstream: route.Doc.Upstream,
				})
			case errors.Is(err, context.Canceled):
				// O cliente desistiu; não há a quem responder.
			default:
				writeDiag(w, http.StatusBadGateway, id, diag{
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

// forwardingHeaders são os cabeçalhos que o Rewrite do ReverseProxy remove
// da requisição de saída antes de chamar a função de reescrita.
var forwardingHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"}

// incomingForwarding copia da requisição de entrada os cabeçalhos de
// encaminhamento que chegaram preenchidos. Os que o cliente declarou em
// Connection são hop-by-hop nessa conexão e ficam de fora.
func incomingForwarding(in http.Header) http.Header {
	hop := map[string]bool{}
	for _, v := range in.Values("Connection") {
		for tok := range strings.SplitSeq(v, ",") {
			if tok = strings.TrimSpace(tok); tok != "" {
				hop[http.CanonicalHeaderKey(tok)] = true
			}
		}
	}
	out := http.Header{}
	for _, k := range forwardingHeaders {
		if v, ok := in[k]; ok && !hop[k] {
			out[k] = slices.Clone(v)
		}
	}
	return out
}

// filled informa se algum dos valores do cabeçalho tem conteúdo.
func filled(v []string) bool {
	return slices.ContainsFunc(v, func(s string) bool { return strings.TrimSpace(s) != "" })
}

func rewriteFor(route *config.CompiledRoute, id Ident) func(*httputil.ProxyRequest) {
	return func(pr *httputil.ProxyRequest) {
		if route.Doc.StripPrefix {
			u := pr.Out.URL
			u.Path = route.Path.Strip(u.Path)
			if u.RawPath != "" {
				u.RawPath = route.Path.Strip(u.RawPath)
			}
		}
		pr.SetURL(route.Upstream)
		// Rewrite descarta os cabeçalhos de encaminhamento de entrada.
		// Recolocar o X-Forwarded-For antes de SetXForwarded faz o endereço do
		// cliente ser acrescentado à cadeia; os demais que já chegaram
		// preenchidos voltam depois, prevalecendo sobre os que SetXForwarded
		// preencheu, que assim só valem para os ausentes. Um X-Forwarded-*
		// que chegou vazio não está preenchido e fica com o valor calculado;
		// o Forwarded, que SetXForwarded não preenche, volta como chegou.
		in := incomingForwarding(pr.In.Header)
		if v := in["X-Forwarded-For"]; filled(v) {
			pr.Out.Header["X-Forwarded-For"] = v
		}
		pr.SetXForwarded()
		if v, ok := in["Forwarded"]; ok {
			pr.Out.Header["Forwarded"] = v
		}
		for _, k := range []string{"X-Forwarded-Host", "X-Forwarded-Proto"} {
			if v := in[k]; filled(v) {
				pr.Out.Header[k] = v
			}
		}
		// SetURL troca o Host pelo do upstream; sem rewriteHost, o Host
		// original é devolvido para que o upstream o receba como chegou.
		if !route.Doc.RewriteHost {
			pr.Out.Host = pr.In.Host
		}
		pr.Out.Header.Set(HeaderGateway, id.String())
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

func writeDiag(w http.ResponseWriter, status int, id Ident, d diag) {
	w.Header().Set(HeaderGateway, id.String())
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
	writeDiag(w, http.StatusNotFound, Ident{}, diag{
		Error:    "no_route",
		Message:  "nenhuma rota casou com a requisição",
		Patterns: patterns,
	})
}
