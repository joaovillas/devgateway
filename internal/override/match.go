// Package override decide se um override da rota intervém numa requisição:
// seleciona o override ligado mais específico que casa (passo 3 do caminho da
// requisição), sorteia aplicação, queda e atraso de uma fonte de
// aleatoriedade própria da requisição (passo 4) e prepara a resposta
// declarada.
package override

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/gamerjp64/gateway/internal/config"
)

// MaxBodyMatchBytes limita quanto do corpo da requisição é lido para avaliar
// um critério de corpo. Um corpo maior não casa com nenhum critério de corpo,
// e segue inteiro para o upstream do mesmo jeito.
const MaxBodyMatchBytes = 8 << 20

// Request é a requisição sob avaliação. A query é interpretada e o corpo é
// lido só quando algum critério precisa deles, uma única vez.
type Request struct {
	r *http.Request

	query url.Values

	bodyRead bool
	body     []byte
	// bodyOK informa que o corpo foi lido inteiro, dentro do limite.
	bodyOK bool
}

// NewRequest prepara a avaliação da requisição.
func NewRequest(r *http.Request) *Request { return &Request{r: r} }

// Request devolve a requisição a usar daqui em diante. Se o corpo foi lido
// para casar, ela carrega um corpo que entrega de novo exatamente os mesmos
// bytes, seguidos do que ainda não havia sido lido, para que o upstream o
// receba íntegro.
func (q *Request) Request() *http.Request {
	if !q.bodyRead || q.r.Body == nil || q.r.Body == http.NoBody {
		return q.r
	}
	r2 := *q.r
	r2.Body = &replayBody{Reader: io.MultiReader(bytes.NewReader(q.body), q.r.Body), c: q.r.Body}
	return &r2
}

// replayBody devolve os bytes já lidos e depois o restante do corpo original,
// que é quem se fecha.
type replayBody struct {
	io.Reader
	c io.Closer
}

func (b *replayBody) Close() error { return b.c.Close() }

func (q *Request) queryValues() url.Values {
	if q.query == nil {
		q.query = q.r.URL.Query()
	}
	return q.query
}

// readBody lê o corpo até o limite. Um corpo acima do limite ou uma falha de
// leitura deixam bodyOK falso: nenhum critério de corpo casa, e o que foi lido
// é devolvido ao encaminhamento por Request.
func (q *Request) readBody() ([]byte, bool) {
	if q.bodyRead {
		return q.body, q.bodyOK
	}
	q.bodyRead = true
	if q.r.Body == nil || q.r.Body == http.NoBody {
		q.bodyOK = true
		return nil, true
	}
	b, err := io.ReadAll(io.LimitReader(q.r.Body, MaxBodyMatchBytes+1))
	q.body = b
	q.bodyOK = err == nil && len(b) <= MaxBodyMatchBytes
	if err != nil && !errors.Is(err, io.EOF) {
		// Quem encaminhar lerá o restante e observará a mesma falha.
		q.bodyOK = false
	}
	return q.body, q.bodyOK
}

// Select devolve o override que vale para a requisição: entre os ligados que
// a selecionam, o mais específico. Os overrides da rota já estão em ordem de
// precedência (path exato; parâmetros de segmento, do de mais literais ao de
// menos; expressão regular; curinga do mais longo ao mais curto; mais
// critérios; ordem de declaração), então vale o primeiro que casa. Devolve nil
// quando nenhum casa.
func Select(route *config.CompiledRoute, q *Request) *config.CompiledOverride {
	return SelectWhere(route, q, nil)
}

// SelectWhere é Select restrito aos overrides aceitos por ok: os recusados
// ficam fora da seleção e da precedência, como os desligados, e não escondem
// um menos específico que também case. Com ok nil, vale Select.
func SelectWhere(route *config.CompiledRoute, q *Request, ok func(*config.CompiledOverride) bool) *config.CompiledOverride {
	for _, o := range route.Overrides {
		if !o.Doc.Enabled() || (ok != nil && !ok(o)) {
			continue
		}
		if Match(o, q) {
			return o
		}
	}
	return nil
}

// Match informa se o override seleciona a requisição: todos os critérios
// declarados precisam casar. O corpo é avaliado por último, porque é o único
// critério que exige ler a requisição.
func Match(o *config.CompiledOverride, q *Request) bool {
	r := q.r
	path := r.URL.Path
	switch {
	case o.PathRegex != nil:
		if !o.PathRegex.MatchString(path) {
			return false
		}
	case !o.Path.Match(path):
		return false
	}
	if m := o.Doc.Match.Method; m != "" && r.Method != m {
		return false
	}
	for k, m := range o.Headers {
		if !anyMatch(m, headerValues(r, k)) {
			return false
		}
	}
	if len(o.Query) > 0 {
		qv := q.queryValues()
		for k, m := range o.Query {
			if !anyMatch(m, qv[k]) {
				return false
			}
		}
	}
	if o.Body != nil {
		b, ok := q.readBody()
		if !ok || !o.Body.MatchString(string(b)) {
			return false
		}
	}
	return true
}

// headerValues devolve os valores do cabeçalho de nome canônico k. O Host,
// que o servidor tira dos cabeçalhos, vem do campo próprio da requisição.
func headerValues(r *http.Request, k string) []string {
	if k == "Host" {
		return []string{r.Host}
	}
	return r.Header.Values(k)
}

// anyMatch casa quando algum dos valores observados satisfaz o operador. Um
// cabeçalho ou parâmetro ausente não casa com nenhum operador.
func anyMatch(m config.CompiledMatcher, vs []string) bool {
	for _, v := range vs {
		if m.MatchString(v) {
			return true
		}
	}
	return false
}
