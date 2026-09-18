package override

import (
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/gamerjp64/gateway/internal/config"
)

// Source devolve a fonte de aleatoriedade da requisição de número de
// sequência seq. Com seed, ela é derivada só de (seed, seq): as decisões de
// uma requisição não dependem de nenhuma outra nem da ordem de escalonamento
// das goroutines, e o mesmo seed com a mesma ordem de chegada reproduz as
// mesmas decisões. Sem seed, a fonte é semeada ao acaso e não se reproduz.
func Source(seed *uint64, seq uint64) *rand.Rand {
	if seed == nil {
		return rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	}
	// Seeds e sequências vizinhas diferem em poucos bits; o espalhamento
	// evita que os primeiros valores do PCG saiam correlacionados.
	return rand.New(rand.NewPCG(mix(*seed), mix(seq)))
}

// mix é o finalizador do splitmix64: uma bijeção que espalha cada bit da
// entrada por toda a saída.
func mix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// Decision é o resultado do sorteio do passo 4 para o override selecionado.
type Decision struct {
	// Override é o override selecionado; nil quando nenhum casou.
	Override *config.CompiledOverride
	// Apply informa que o override vale nesta requisição. Falso, a requisição
	// segue como se o override não existisse, e Drop e Delay são zero.
	Apply bool
	// Drop informa que a conexão deve ser derrubada sem resposta.
	Drop bool
	// Delay é o atraso a injetar entre a resposta pronta e a escrita ao
	// cliente.
	Delay time.Duration
}

// Applied devolve o override que vale nesta requisição, ou nil.
func (d Decision) Applied() *config.CompiledOverride {
	if !d.Apply {
		return nil
	}
	return d.Override
}

// Decide sorteia, em ordem fixa e da fonte dada, a aplicação, a queda e o
// atraso do override. Os três valores são sempre consumidos, nesta ordem,
// mesmo quando a configuração não precisa de algum deles: assim a posição de
// cada sorteio na sequência não depende da configuração nem do que acontece
// depois (disponibilidade do upstream, por exemplo). Com o override nil a
// decisão é vazia e nada é sorteado.
func Decide(o *config.CompiledOverride, rng *rand.Rand) Decision {
	if o == nil {
		return Decision{}
	}
	apply := rng.Float64()
	// A queda é declarada, não sorteada; a posição dela fica reservada para
	// que o atraso ocupe sempre o terceiro valor.
	rng.Float64()
	delay := rng.Float64()
	p := 1.0
	if o.Doc.Probability != nil {
		p = *o.Doc.Probability
	}
	d := Decision{Override: o, Apply: apply < p}
	if !d.Apply {
		return d
	}
	d.Drop = o.Doc.Drop
	if l := o.Doc.Latency; l != nil {
		switch {
		case l.Fixed != nil:
			d.Delay = time.Duration(*l.Fixed)
		case l.Min != nil && l.Max != nil:
			lo, hi := time.Duration(*l.Min), time.Duration(*l.Max)
			d.Delay = lo + time.Duration(delay*float64(hi-lo))
		}
	}
	return d
}

// Status é o status da resposta declarada: 200 quando não informado.
func Status(o *config.CompiledOverride) int {
	if r := o.Doc.Respond; r != nil && r.Status != 0 {
		return r.Status
	}
	return http.StatusOK
}

// Body é o corpo da resposta declarada, já serializado.
func Body(o *config.CompiledOverride) []byte { return o.RespondBody }

// SetHeaders acrescenta a h os cabeçalhos da resposta declarada. Um corpo
// declarado como estrutura leva o tipo de conteúdo JSON, salvo se o override
// declara outro. Sem tipo de conteúdo declarado nem inferido, o servidor não
// deduz um pelo corpo: o cliente recebe só o que o override declarou.
func SetHeaders(h http.Header, o *config.CompiledOverride) {
	declared := false
	if r := o.Doc.Respond; r != nil {
		// Um cabeçalho com vários valores é repetido na resposta, um por
		// valor, na ordem declarada.
		for k, vs := range r.Headers {
			for _, v := range vs {
				h.Add(k, v)
			}
			if strings.EqualFold(k, "Content-Type") {
				declared = true
			}
		}
	}
	switch {
	case declared:
	case o.RespondContentType != "":
		h.Set("Content-Type", o.RespondContentType)
	default:
		h["Content-Type"] = nil
	}
}
