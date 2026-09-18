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

	"github.com/gamerjp64/gateway/internal/capture"
	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/exchange"
	"github.com/gamerjp64/gateway/internal/override"
	"github.com/gamerjp64/gateway/internal/upstream"
)

// DefaultTimeout vale para rotas que não declaram timeout.
const DefaultTimeout = 30 * time.Second

// Handler é o handler da porta de tráfego.
type Handler struct {
	live      *config.Live
	rec       *capture.Recorder
	transport http.RoundTripper
	// delayFor decide o atraso a injetar entre a resposta pronta e a escrita
	// ao cliente, com o override responsável. Sem ele, ou sem override que
	// atrase, o atraso é zero.
	delayFor func(*http.Request) (override string, d time.Duration)

	tracker   *override.Tracker
	learner   Learner
	upstreams *upstream.Health
}

// Learner aprende endpoints a partir das trocas respondidas pelo upstream.
// Observe é chamado no fim de cada requisição com o modo aprendizado ligado
// e não pode bloquear: a gravação acontece fora do caminho da requisição.
type Learner interface {
	Observe(route *config.CompiledRoute, e exchange.Exchange)
}

// Options completa o handler.
type Options struct {
	// Tracker guarda o estado vivo dos overrides (expiração por tempo e por
	// contagem). Sem ele, o handler cria o seu.
	Tracker *override.Tracker
	// Learner recebe as trocas do modo aprendizado. Sem ele, nada é
	// aprendido.
	Learner Learner
	// Upstreams recebe o resultado de cada tentativa de encaminhamento,
	// para a disponibilidade recente dos upstreams. Sem ele, nada é contado.
	Upstreams *upstream.Health
}

// NewHandler atende o tráfego com a configuração em vigor em live e registra
// as trocas em rec.
func NewHandler(live *config.Live, rec *capture.Recorder) *Handler {
	return NewHandlerWith(live, rec, Options{})
}

// NewHandlerWith é NewHandler com as opções dadas.
func NewHandlerWith(live *config.Live, rec *capture.Recorder, o Options) *Handler {
	if o.Tracker == nil {
		o.Tracker = override.NewTracker(live)
	}
	return &Handler{live: live, rec: rec, transport: newTransport(), tracker: o.Tracker, learner: o.Learner, upstreams: o.Upstreams}
}

// Tracker devolve o estado vivo dos overrides usado pelo handler.
func (h *Handler) Tracker() *override.Tracker { return h.tracker }

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

// ServeHTTP percorre o caminho da requisição na ordem fixa do design: resolve
// a rota (1), abre o registro de captura (2), resolve o override ligado mais
// específico que casa (3), sorteia aplicação, queda e atraso (4), segue como
// se o override não existisse quando a aplicação não é sorteada (5),
// sintetiza a resposta declarada ou encaminha ao upstream (7), aplica o
// atraso entre a resposta pronta e a escrita ao cliente (8) e fecha o
// registro com os tempos decompostos (9), entregando a troca ao aprendizado
// quando ele está ligado. A queda de conexão (6) encerra a requisição antes
// de qualquer resposta.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// O snapshot é capturado uma única vez: uma recarga no meio da requisição
	// não a afeta.
	snap := h.live.Load()
	route := Resolve(snap, r)

	learning := h.learner != nil && snap.Settings.LearningEnabled && route != nil && route.Upstream != nil
	rec := h.rec.Begin(w, r, capture.Options{
		Record:       snap.Settings.HistoryRecord,
		Observe:      learning,
		MaxBodyBytes: snap.Settings.CaptureMaxBodyBytes,
	})
	defer func() {
		// Uma cópia de corpo interrompida faz o ReverseProxy abortar o
		// handler com pânico, assim como a queda em HTTP/2; a troca é
		// registrada antes de o pânico seguir.
		if p := recover(); p != nil {
			rec.Abort("a transferência da resposta foi interrompida")
			rec.Finish()
			panic(p)
		}
		rec.Finish()
		// Passo 9: o aprendizado recebe a troca já respondida e grava fora
		// do caminho da requisição.
		if learning {
			if e, ok := rec.Exchange(); ok {
				// Com o registro desligado a troca não vai para o histórico:
				// o override aprendido não pode apontar para ela.
				if !rec.Enabled() {
					e.ID = ""
				}
				h.learner.Observe(route, e)
			}
		}
	}()
	w, r = rec.Writer(), rec.Request()

	if route == nil {
		rec.DrainRequest()
		d := writeNoRoute(w, snap)
		rec.Fail(d.Error + ": " + d.Message)
		return
	}
	rec.SetRoute(route.Name(), route.Doc.Upstream)

	dec := h.decide(snap, route, &r, rec)

	// Passo 6: a queda tem precedência sobre a resposta declarada.
	if o := dec.Applied(); o != nil && dec.Drop {
		h.drop(w, r, o, rec)
		return
	}

	// Passo 7.
	if o := dec.Applied(); o != nil && o.Doc.Respond != nil {
		h.synthesize(w, r, route, dec, rec)
		return
	}
	if route.Upstream == nil {
		rec.DrainRequest()
		d := diag{
			Error:   "no_upstream",
			Message: "a rota não declara upstream e nenhum override interceptou a requisição",
			Route:   route.Name(),
		}
		rec.Fail(d.Error + ": " + d.Message)
		id := h.ident(route, dec, "")
		if h.delay(r, dec, rec) != nil {
			return
		}
		writeDiag(w, http.StatusNotImplemented, id, d)
		return
	}
	h.forward(w, r, route, dec, rec)
}

// decide executa os passos 3 a 5. Passo 3: entre os overrides ligados e não
// expirados que selecionam a requisição, o mais específico. Um critério de
// corpo lê a requisição; *r passa a ser uma requisição que entrega o corpo de
// novo, íntegro, a quem encaminhar. Passos 4 e 5: o sorteio acontece antes de
// qualquer contato com o upstream, para que o mesmo seed produza as mesmas
// decisões qualquer que seja a disponibilidade dele. Sem aplicação sorteada,
// dec.Applied é nil e a requisição segue como se o override não existisse.
//
// A aplicação sorteada é contada no estado vivo do override. Se, entre a
// seleção e a contagem, o override se esgotou por outra requisição
// concorrente, ele sai da seleção e a escolha é refeita, com a mesma fonte
// derivada de (seed, sequência), como se ele já estivesse expirado na
// entrada.
func (h *Handler) decide(snap *config.Snapshot, route *config.CompiledRoute, r **http.Request, rec *capture.Record) override.Decision {
	q := override.NewRequest(*r)
	defer func() { *r = q.Request() }()
	var exhausted []*config.CompiledOverride
	for {
		selected := override.SelectWhere(route, q, func(o *config.CompiledOverride) bool {
			return !slices.Contains(exhausted, o) && h.tracker.Active(o)
		})
		if selected == nil {
			return override.Decision{}
		}
		dec := override.Decide(selected, override.Source(snap.Settings.Seed, rec.Seq()))
		if !dec.Apply || h.tracker.Claim(selected) {
			return dec
		}
		exhausted = append(exhausted, selected)
	}
}

// drop encerra a conexão sem enviar resposta. Em HTTP/1.x o socket é
// sequestrado e fechado com reset; em HTTP/2, onde não há socket próprio da
// requisição, o handler é abortado com http.ErrAbortHandler e o servidor
// cancela o stream. A captura registra qual dos dois aconteceu. O corpo da
// requisição é lido antes, para constar da captura.
func (h *Handler) drop(w http.ResponseWriter, r *http.Request, o *config.CompiledOverride, rec *capture.Record) {
	rec.DrainRequest()
	mode := exchange.DropStreamReset
	if r.ProtoMajor < 2 {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			rec.Dropped(o.ID(), exchange.DropHijack)
			if tc, ok := conn.(*net.TCPConn); ok {
				// Sem espera pelo envio pendente: o cliente observa o reset.
				tc.SetLinger(0)
			}
			conn.Close()
			return
		}
		mode = exchange.DropAbort
	}
	rec.Dropped(o.ID(), mode)
	panic(http.ErrAbortHandler)
}

// synthesize responde com a resposta declarada pelo override, sem contato com
// o upstream. O corpo da requisição é lido até o fim para constar da captura.
func (h *Handler) synthesize(w http.ResponseWriter, r *http.Request, route *config.CompiledRoute, dec override.Decision, rec *capture.Record) {
	o := dec.Override
	rec.DrainRequest()
	rec.Intervene(o.ID(), "synthesized")
	hdr := w.Header()
	override.SetHeaders(hdr, o)
	hdr.Set(HeaderGateway, h.ident(route, dec, "synthesized").String())
	// Passo 8: a resposta está pronta; o atraso vem antes de escrevê-la.
	if h.delay(r, dec, rec) != nil {
		// Nada chegou ao cliente: a troca fica sem status, com a desistência
		// anotada, como no encaminhamento.
		rec.Abort("client_canceled: o cliente desistiu durante o atraso injetado")
		return
	}
	w.WriteHeader(override.Status(o))
	w.Write(override.Body(o))
}

// ident monta o X-Gateway da resposta com as intervenções aplicadas, na
// ordem em que acontecem: synthesized quando o override sintetizou a
// resposta, delayed quando a atrasou, e as duas, separadas por vírgula
// ("synthesized,delayed"), quando fez ambas. Sem intervenção, o cabeçalho
// identifica apenas a rota.
func (h *Handler) ident(route *config.CompiledRoute, dec override.Decision, kind string) Ident {
	id := Ident{Route: route.Name()}
	o := dec.Applied()
	if o == nil {
		return id
	}
	if dec.Delay > 0 {
		if kind != "" {
			kind += ","
		}
		kind += "delayed"
	}
	if kind != "" {
		id.Override, id.Intervention = o.ID(), kind
	}
	return id
}

// delay aplica o atraso sorteado para a requisição, no ponto entre a resposta
// pronta e a escrita ao cliente. O gancho delayFor, quando presente, decide
// no lugar do sorteio.
func (h *Handler) delay(r *http.Request, dec override.Decision, rec *capture.Record) error {
	if h.delayFor != nil {
		name, d := h.delayFor(r)
		return rec.Delay(r.Context(), name, d)
	}
	o := dec.Applied()
	if o == nil {
		return nil
	}
	return rec.Delay(r.Context(), o.ID(), dec.Delay)
}

// forward encaminha ao upstream. O tempo limite da rota vale até a chegada
// dos cabeçalhos da resposta: depois disso o corpo pode ser um stream longo.
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, route *config.CompiledRoute, dec override.Decision, rec *capture.Record) {
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
	rewrite := rewriteFor(route, id)
	rp := &httputil.ReverseProxy{
		Transport: h.transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			rewrite(pr)
			rec.UpstreamStarted()
		},
		ModifyResponse: func(res *http.Response) error {
			timer.Stop()
			// O upstream respondeu, qualquer que seja o status.
			h.upstreams.Success(route.Doc.Upstream)
			res.Header.Set(HeaderGateway, h.ident(route, dec, "").String())
			// A resposta está pronta; o atraso vem antes de escrevê-la.
			if err := h.delay(r, dec, rec); err != nil {
				return err
			}
			if res.StatusCode == http.StatusSwitchingProtocols {
				// Num upgrade o ReverseProxy escreve a resposta direto na
				// conexão sequestrada, sem passar pelo writer da captura.
				rec.Upgrade(res.StatusCode, res.Header)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			rec.UpstreamDone()
			var d diag
			status := 0
			switch {
			case timedOut.Load():
				d = diag{
					Error:    "upstream_timeout",
					Message:  "o upstream excedeu o tempo limite de " + timeout.String(),
					Route:    route.Name(),
					Upstream: route.Doc.Upstream,
				}
				status = http.StatusGatewayTimeout
				h.upstreams.Failure(route.Doc.Upstream, d.Message)
			case errors.Is(err, context.Canceled):
				// O cliente desistiu (inclusive durante o atraso injetado em
				// ModifyResponse); não há a quem responder.
				d = diag{Error: "client_canceled", Message: "o cliente desistiu antes da resposta"}
			default:
				d = diag{
					Error:    "upstream_unavailable",
					Message:  "não foi possível obter resposta do upstream: " + err.Error(),
					Route:    route.Name(),
					Upstream: route.Doc.Upstream,
				}
				status = http.StatusBadGateway
				h.upstreams.Failure(route.Doc.Upstream, err.Error())
			}
			rec.Fail(d.Error + ": " + d.Message)
			if status == 0 {
				return
			}
			// Passo 8 também para a resposta de erro do gateway: pronta a
			// resposta, o atraso do override vem antes de escrevê-la. Nesses
			// casos ModifyResponse não chegou a ser chamado e o atraso ainda
			// não foi aplicado.
			if h.delay(r, dec, rec) != nil {
				rec.Fail("client_canceled: o cliente desistiu durante o atraso injetado")
				return
			}
			writeDiag(w, status, h.ident(route, dec, ""), d)
		},
	}
	rp.ServeHTTP(w, r.WithContext(ctx))
	rec.UpstreamDone()
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

func writeNoRoute(w http.ResponseWriter, snap *config.Snapshot) diag {
	patterns := make([]string, 0, len(snap.Routes))
	for _, r := range snap.Routes {
		patterns = append(patterns, r.Pattern())
	}
	d := diag{
		Error:    "no_route",
		Message:  "nenhuma rota casou com a requisição",
		Patterns: patterns,
	}
	writeDiag(w, http.StatusNotFound, Ident{}, d)
	return d
}
