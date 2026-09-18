// Package exchange define a troca HTTP registrada pelo gateway e o filtro
// usado para consultá-la, compartilhados pela captura, pelo armazenamento e
// pela API.
package exchange

import (
	"crypto/rand"
	"net/http"
	"strings"
	"time"
)

// Outcome diz o que produziu o resultado da troca.
type Outcome string

const (
	OutcomeUpstream    Outcome = "upstream"    // resposta do upstream
	OutcomeSynthesized Outcome = "synthesized" // resposta declarada por override
	OutcomeDropped     Outcome = "dropped"     // conexão derrubada por override
	OutcomeGateway     Outcome = "gateway"     // resposta de erro do próprio gateway
)

// Exchange é uma troca HTTP que atravessou a porta de tráfego.
type Exchange struct {
	ID    string    `json:"id"`
	Seq   uint64    `json:"seq"` // número de sequência de chegada no processo
	Start time.Time `json:"start"`

	Method     string `json:"method"`
	Host       string `json:"host"`
	Path       string `json:"path"`
	Query      string `json:"query,omitempty"`
	ClientAddr string `json:"clientAddr,omitempty"`

	Route    string `json:"route,omitempty"`
	Upstream string `json:"upstream,omitempty"`
	// Override é "rota/override" quando algum override interveio.
	Override string `json:"override,omitempty"`
	// Interventions lista o que o override fez: synthesized, delayed, dropped.
	Interventions []string `json:"interventions,omitempty"`
	Outcome       Outcome  `json:"outcome"`
	// DropMode registra como a queda aconteceu: hijack (HTTP/1.1) ou
	// stream_reset (HTTP/2, onde não há socket para fechar).
	DropMode string `json:"dropMode,omitempty"`
	// Error descreve falhas do gateway ou do upstream (502, 504...).
	Error string `json:"error,omitempty"`

	Status   int     `json:"status,omitempty"` // zero quando não houve resposta
	Request  Message `json:"request"`
	Response Message `json:"response"`

	Timing Timing `json:"timing"`
}

// Intervened informa se um override alterou o resultado desta troca.
func (e *Exchange) Intervened() bool { return len(e.Interventions) > 0 }

// Message é um lado da troca: cabeçalhos e corpo capturado.
type Message struct {
	Headers http.Header `json:"headers,omitempty"`
	Body    []byte      `json:"body,omitempty"`
	// Size é o tamanho real do corpo em bytes, mesmo quando truncado.
	Size      int64 `json:"size"`
	Truncated bool  `json:"truncated,omitempty"`
}

// Timing decompõe a latência da troca, em milissegundos.
//
// O tempo de upstream vai do envio da requisição ao upstream até o fim do
// corpo da resposta, descontado o atraso injetado nessa janela. O corpo é
// repassado ao cliente em streaming, sem ser acumulado: quando o cliente lê
// mais devagar do que o upstream envia, a cópia espera pelo cliente, e essa
// espera entra no tempo de upstream. Separá-las exigiria acumular a resposta
// inteira antes de escrevê-la, o que quebraria o streaming.
type Timing struct {
	TotalMs    float64 `json:"totalMs"`
	UpstreamMs float64 `json:"upstreamMs"`
	InjectedMs float64 `json:"injectedMs"`
	GatewayMs  float64 `json:"gatewayMs"`
}

// Ms converte uma duração para milissegundos.
func Ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// Filter restringe a consulta. Campos vazios não filtram; os declarados
// valem em conjunto.
type Filter struct {
	Route    string
	Upstream string
	Override string
	Method   string
	// Path casa por conteúdo: "/charge" encontra "/api/payments/charge/1".
	Path      string
	StatusMin int
	StatusMax int
	// Intervened, quando declarado, separa trocas com e sem intervenção.
	Intervened *bool
	Since      time.Time
	Until      time.Time
}

// Match aplica o filtro a uma troca. Os backends que filtram fora do Go
// precisam reproduzir exatamente esta semântica.
func (f Filter) Match(e *Exchange) bool {
	switch {
	case f.Route != "" && e.Route != f.Route:
		return false
	case f.Upstream != "" && e.Upstream != f.Upstream:
		return false
	case f.Override != "" && e.Override != f.Override:
		return false
	case f.Method != "" && !strings.EqualFold(e.Method, f.Method):
		return false
	case f.Path != "" && !strings.Contains(e.Path, f.Path):
		return false
	case f.StatusMin != 0 && e.Status < f.StatusMin:
		return false
	case f.StatusMax != 0 && e.Status > f.StatusMax:
		return false
	case f.Intervened != nil && e.Intervened() != *f.Intervened:
		return false
	case !f.Since.IsZero() && e.Start.Before(f.Since):
		return false
	case !f.Until.IsZero() && !e.Start.Before(f.Until):
		return false
	}
	return true
}

// Summary é a troca sem os corpos, para listagens.
func (e Exchange) Summary() Exchange {
	e.Request.Body = nil
	e.Response.Body = nil
	return e
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID gera um identificador no formato ULID: 26 caracteres, ordenável
// pelo instante de criação e único entre processos.
func NewID(t time.Time) string {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	rand.Read(b[6:])
	// 128 bits em 26 dígitos base32, do mais significativo ao menos.
	var out [26]byte
	var acc uint64
	bits := 0
	j := 25
	for i := 15; i >= 0; i-- {
		acc |= uint64(b[i]) << bits
		bits += 8
		for bits >= 5 && j >= 0 {
			out[j] = crockford[acc&31]
			acc >>= 5
			bits -= 5
			j--
		}
	}
	for j >= 0 {
		out[j] = crockford[acc&31]
		acc >>= 5
		j--
	}
	return string(out[:])
}
