package capture

import (
	"context"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/gamerjp64/gateway/internal/exchange"
)

// Record é o registro de uma requisição em curso. É usado pela goroutine que
// atende a requisição; só o corpo da requisição, lido também pelo transporte
// do upstream, tem proteção própria.
type Record struct {
	rec *Recorder
	// enabled liga a observação da troca; store, a gravação no histórico.
	enabled, store bool
	seq            uint64
	start          time.Time

	w    http.ResponseWriter
	req  *http.Request
	cw   *writer
	body *bodyTap

	ex exchange.Exchange

	// upgradeStatus e upgradeHeader guardam a resposta de um upgrade de
	// protocolo, que o ReverseProxy escreve direto na conexão sequestrada,
	// sem passar pelo writer da captura.
	upgradeStatus int
	upgradeHeader http.Header

	upStart, upEnd time.Time
	// injected é o atraso injetado medido; injectedUp é a parte dele que
	// aconteceu dentro da janela do upstream e dela é descontada.
	injected, injectedUp time.Duration
	finished             bool
}

// Seq é o número de sequência de chegada da requisição no processo.
func (rec *Record) Seq() uint64 { return rec.seq }

// Enabled informa se esta requisição está sendo registrada no histórico.
func (rec *Record) Enabled() bool { return rec.store }

// Writer é o ResponseWriter a usar daqui em diante: com o registro ligado,
// ele observa status, cabeçalhos e corpo sem alterar o que chega ao cliente.
func (rec *Record) Writer() http.ResponseWriter { return rec.w }

// Request é a requisição a usar daqui em diante: com o registro ligado, o
// corpo é observado à medida que é lido, e chega inteiro a quem o ler.
func (rec *Record) Request() *http.Request { return rec.req }

// SetRoute anota a rota casada e o upstream de destino.
func (rec *Record) SetRoute(route, upstream string) {
	rec.ex.Route = route
	rec.ex.Upstream = upstream
}

// Intervene anota o override responsável ("rota/override") e o que ele fez:
// synthesized, delayed ou dropped. Sintetizar ou derrubar define o resultado
// da troca.
func (rec *Record) Intervene(override, kind string) {
	if override != "" {
		rec.ex.Override = override
	}
	if !slices.Contains(rec.ex.Interventions, kind) {
		rec.ex.Interventions = append(rec.ex.Interventions, kind)
	}
	switch kind {
	case "synthesized":
		rec.ex.Outcome = exchange.OutcomeSynthesized
	case "dropped":
		rec.ex.Outcome = exchange.OutcomeDropped
	}
}

// Dropped anota uma queda de conexão e como ela aconteceu (hijack ou
// stream_reset). A troca fica sem status de resposta.
func (rec *Record) Dropped(override, mode string) {
	rec.Intervene(override, "dropped")
	rec.ex.DropMode = mode
}

// Fail marca a troca como resposta de erro do próprio gateway (sem rota,
// sem upstream, upstream indisponível ou lento), com a descrição da falha.
func (rec *Record) Fail(msg string) {
	rec.ex.Outcome = exchange.OutcomeGateway
	rec.ex.Error = msg
}

// Abort anota que a transferência foi interrompida depois de começar, sem
// mudar quem produziu a resposta.
func (rec *Record) Abort(msg string) {
	// Uma queda provocada por override encerra o handler pelo mesmo pânico
	// de uma transferência interrompida, mas não é falha.
	if rec.ex.Outcome == exchange.OutcomeDropped {
		return
	}
	if rec.ex.Error == "" {
		rec.ex.Error = msg
	}
}

// Upgrade anota a resposta de um upgrade de protocolo (101 Switching
// Protocols, como no WebSocket). Nesse caminho o ReverseProxy sequestra a
// conexão e escreve a resposta nela sem chamar o WriteHeader do writer da
// captura; sem esta anotação a troca ficaria com status 200 e sem os
// cabeçalhos. Uma resposta escrita pelo writer depois disso (a falha do
// upgrade, por exemplo) prevalece.
func (rec *Record) Upgrade(status int, h http.Header) {
	if !rec.enabled {
		return
	}
	rec.upgradeStatus = status
	rec.upgradeHeader = h.Clone()
}

// UpstreamStarted marca o envio da requisição ao upstream.
func (rec *Record) UpstreamStarted() {
	if rec.upStart.IsZero() {
		rec.upStart = rec.rec.now()
	}
}

// UpstreamDone marca o fim do contato com o upstream: o fim do corpo da
// resposta ou a falha. Sem UpstreamStarted antes, não tem efeito. Com o
// corpo em streaming, o fim do corpo inclui a espera por um cliente lento
// (ver exchange.Timing).
func (rec *Record) UpstreamDone() {
	if !rec.upStart.IsZero() && rec.upEnd.IsZero() {
		rec.upEnd = rec.rec.now()
	}
}

// Delay é o ponto de injeção de atraso: chamado entre a resposta pronta e a
// escrita ao cliente, espera d e contabiliza o tempo realmente esperado como
// injetado. Com d zero não faz nada. Devolve o erro do contexto se o cliente
// desistir durante a espera.
func (rec *Record) Delay(ctx context.Context, override string, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	rec.Intervene(override, "delayed")
	t0 := rec.rec.now()
	t := time.NewTimer(d)
	defer t.Stop()
	var err error
	select {
	case <-t.C:
	case <-ctx.Done():
		err = ctx.Err()
	}
	waited := rec.rec.now().Sub(t0)
	rec.injected += waited
	if !rec.upStart.IsZero() && rec.upEnd.IsZero() {
		rec.injectedUp += waited
	}
	return err
}

// DrainMaxBytes limita quanto DrainRequest lê além do limite de captura.
// Um corpo maior que isso fica com o tamanho declarado em Content-Length,
// quando há, ou com o que foi lido, e marcado como truncado.
const DrainMaxBytes = 64 << 20

// DrainRequest lê o corpo da requisição até o fim quando ninguém vai
// encaminhá-lo (resposta do próprio gateway), guardando só o começo, até o
// limite de captura, e contando o tamanho real, inclusive de um corpo sem
// Content-Length. Não deve ser chamado depois de a requisição ir ao
// upstream.
func (rec *Record) DrainRequest() {
	if rec.body == nil {
		return
	}
	rec.body.mu.Lock()
	eof := rec.body.eof
	rec.body.mu.Unlock()
	if !eof {
		io.Copy(io.Discard, io.LimitReader(rec.body, int64(rec.body.tap.limit)+DrainMaxBytes))
	}
}

// Finish fecha o registro com os tempos decompostos e, com o registro ligado,
// entrega a troca à gravação, sem esperar por ela. Chamadas repetidas não têm
// efeito.
func (rec *Record) Finish() {
	if rec.finished {
		return
	}
	rec.finished = true
	defer rec.rec.finished()
	end := rec.rec.now()
	if !rec.enabled {
		return
	}
	rec.UpstreamDone()
	e := &rec.ex

	var upstream time.Duration
	if !rec.upStart.IsZero() {
		upstream = max(rec.upEnd.Sub(rec.upStart)-rec.injectedUp, 0)
	}
	total := end.Sub(rec.start)
	e.Timing = exchange.Timing{
		TotalMs:    exchange.Ms(total),
		UpstreamMs: exchange.Ms(upstream),
		InjectedMs: exchange.Ms(rec.injected),
		GatewayMs:  exchange.Ms(max(total-upstream-rec.injected, 0)),
	}

	if rec.body != nil {
		e.Request = rec.body.message(rec.req.ContentLength, e.Request.Headers)
	}
	cw := rec.cw
	header := cw.header
	switch {
	case e.Outcome == exchange.OutcomeDropped:
		e.Status = 0
	case cw.status != 0:
		e.Status = cw.status
	case rec.upgradeStatus != 0:
		e.Status, header = rec.upgradeStatus, rec.upgradeHeader
	case e.Error == "":
		// O handler terminou sem escrever: o servidor responde 200 vazio.
		e.Status = http.StatusOK
	}
	e.Response = cw.tap.message(header)
	if rec.store {
		rec.rec.enqueue(e)
	}
}

// Exchange devolve a troca observada, depois de Finish. Falso quando a troca
// não foi observada. A cópia compartilha cabeçalhos e corpos com a troca
// entregue à gravação e deve ser tratada como somente leitura.
func (rec *Record) Exchange() (exchange.Exchange, bool) {
	if !rec.enabled || !rec.finished {
		return exchange.Exchange{}, false
	}
	return rec.ex, true
}

// tap guarda o começo de um corpo, até limit bytes, e conta o tamanho real.
type tap struct {
	limit int
	buf   []byte
	size  int64
}

func (t *tap) add(p []byte) {
	t.size += int64(len(p))
	if room := t.limit - len(t.buf); room > 0 {
		t.buf = append(t.buf, p[:min(room, len(p))]...)
	}
}

func (t *tap) message(h http.Header) exchange.Message {
	return exchange.Message{
		Headers:   h,
		Body:      t.buf,
		Size:      t.size,
		Truncated: t.size > int64(len(t.buf)),
	}
}

// bodyTap observa o corpo da requisição enquanto ele é lido. O transporte do
// upstream pode ler numa goroutine própria, inclusive depois que o
// ReverseProxy retorna, por isso o estado é protegido.
type bodyTap struct {
	rc  io.ReadCloser
	mu  sync.Mutex
	tap tap
	eof bool
}

func (b *bodyTap) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.mu.Lock()
	b.tap.add(p[:n])
	if err == io.EOF {
		b.eof = true
	}
	b.mu.Unlock()
	return n, err
}

func (b *bodyTap) Close() error { return b.rc.Close() }

// message monta o lado da requisição. Um corpo que não foi lido até o fim
// tem o tamanho declarado em Content-Length, quando há, e fica marcado como
// truncado, porque a captura não viu tudo.
func (b *bodyTap) message(contentLength int64, h http.Header) exchange.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	m := b.tap.message(h)
	m.Body = slices.Clone(m.Body)
	if !b.eof {
		m.Size = max(m.Size, contentLength)
		m.Truncated = true
	}
	return m
}

// writer observa a resposta a caminho do cliente. Implementa Flush e Unwrap
// para que o streaming continue incremental e para que o ReverseProxy ainda
// alcance o Hijacker do servidor num upgrade de protocolo.
type writer struct {
	http.ResponseWriter
	status int
	header http.Header
	tap    tap
}

func (w *writer) WriteHeader(code int) {
	// As respostas informativas passam sem fixar o status; 101 é final.
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.status == 0 {
		w.status = code
		w.header = w.ResponseWriter.Header().Clone()
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *writer) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
		w.header = w.ResponseWriter.Header().Clone()
	}
	n, err := w.ResponseWriter.Write(p)
	w.tap.add(p[:n])
	return n, err
}

// FlushError repassa o Flush ao ResponseWriter do servidor.
func (w *writer) FlushError() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}

// Flush atende a quem usa a interface http.Flusher diretamente.
func (w *writer) Flush() { w.FlushError() }

func (w *writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }
