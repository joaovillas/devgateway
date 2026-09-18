package override

import (
	"bytes"
	"encoding/json"
	"math/big"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/exchange"
)

// hopByHop são os cabeçalhos que valem só para uma conexão e que o HTTP
// proíbe repassar.
var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// excludedHeaders ficam de fora da resposta pré-preenchida: Date e
// Content-Length são recalculados a cada resposta, e X-Gateway é o próprio
// cabeçalho do gateway, que não veio do upstream e é refeito a cada
// intervenção.
var excludedHeaders = []string{"Date", "Content-Length", "X-Gateway"}

// FromExchange monta um override a partir de uma troca respondida pelo
// upstream: path exato e método da requisição como critério, e a resposta
// observada — status, cabeçalhos e corpo — como resposta declarada. Ficam de
// fora dos cabeçalhos apenas Date, Content-Length, o X-Gateway do próprio
// gateway e os hop-by-hop, inclusive os nomeados em Connection. Um cabeçalho
// repetido, como vários Set-Cookie, mantém todos os valores, na ordem
// observada. O corpo JSON vira estrutura; qualquer outro, texto. source
// registra kind, a troca (quando e.ID não é vazio) e o instante at, e se o
// corpo foi truncado na captura. O nome fica a cargo de quem grava o override
// (ver Name).
func FromExchange(e exchange.Exchange, kind string, at time.Time) config.Override {
	resp := &config.Respond{Status: e.Status, Headers: responseHeaders(e.Response.Headers)}
	if b := e.Response.Body; len(b) > 0 {
		resp.Body = responseBody(e.Response.Headers.Get("Content-Type"), b, e.Response.Truncated)
	}
	return config.Override{
		Match:   ExactMatch(e.Method, e.Path),
		Respond: resp,
		Source: &config.OverrideSource{
			Kind:           kind,
			Exchange:       e.ID,
			At:             at.UTC(),
			BodyIncomplete: e.Response.Truncated,
		},
	}
}

// ExactMatch é o critério que seleciona exatamente o método e o path dados.
// No padrão de path o * é curinga; um path que contém * literal, ou que não
// começa com /, não pode ser escrito como path exato e vira uma expressão
// regular ancorada que casa só com ele.
func ExactMatch(method, path string) config.OverrideMatch {
	if strings.HasPrefix(path, "/") && !strings.Contains(path, "*") {
		return config.OverrideMatch{Path: path, Method: method}
	}
	return config.OverrideMatch{PathRegex: "^" + regexp.QuoteMeta(path) + "$", Method: method}
}

func responseHeaders(h http.Header) map[string]config.HeaderValues {
	skip := map[string]bool{}
	for _, k := range slices.Concat(hopByHop, excludedHeaders) {
		skip[k] = true
	}
	for _, v := range h.Values("Connection") {
		for tok := range strings.SplitSeq(v, ",") {
			if tok = strings.TrimSpace(tok); tok != "" {
				skip[http.CanonicalHeaderKey(tok)] = true
			}
		}
	}
	out := map[string]config.HeaderValues{}
	for k, vs := range h {
		if skip[http.CanonicalHeaderKey(k)] || len(vs) == 0 {
			continue
		}
		out[k] = slices.Clone(vs)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// responseBody devolve o corpo como estrutura quando o tipo de conteúdo é
// JSON e o corpo, completo, é JSON válido cujos números cabem sem perda na
// estrutura; senão, como texto, byte a byte como observado.
func responseBody(contentType string, b []byte, truncated bool) any {
	if !truncated && isJSONType(contentType) {
		if v, ok := decodeJSON(b); ok {
			return v
		}
	}
	return string(b)
}

func isJSONType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

// decodeJSON interpreta o corpo preservando os números: um inteiro que cabe
// em int64 ou uint64 fica inteiro, e os demais viram float64. Se algum número
// não é representável sem perda (um inteiro maior que uint64, um decimal com
// mais dígitos do que o float64 guarda), o corpo não é aceito como estrutura,
// para que a resposta pré-preenchida não difira da observada.
func decodeJSON(b []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return nil, false
	}
	return numbers(v)
}

func numbers(v any) (any, bool) {
	switch x := v.(type) {
	case json.Number:
		return number(x.String())
	case map[string]any:
		for k, e := range x {
			n, ok := numbers(e)
			if !ok {
				return nil, false
			}
			x[k] = n
		}
	case []any:
		for i, e := range x {
			n, ok := numbers(e)
			if !ok {
				return nil, false
			}
			x[i] = n
		}
	}
	return v, true
}

// number converte o literal JSON s sem perda, ou informa que não é possível.
func number(s string) (any, bool) {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i, true
	}
	if u, err := strconv.ParseUint(s, 10, 64); err == nil {
		return u, true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, false
	}
	// O float64 reproduz o literal quando a sua representação mais curta
	// tem o mesmo valor exato que o texto observado.
	want, ok1 := new(big.Rat).SetString(s)
	got, ok2 := new(big.Rat).SetString(strconv.FormatFloat(f, 'g', -1, 64))
	if !ok1 || !ok2 || want.Cmp(got) != 0 {
		return nil, false
	}
	return f, true
}

// Known informa se a rota já tem um override, ligado ou desligado, que
// seleciona exatamente o método e o path dados, na forma que ExactMatch
// produz. Curingas, outras expressões regulares e overrides sem método não
// contam, para que endpoints sob eles também sejam aprendidos.
func Known(r config.Route, method, path string) bool {
	want := ExactMatch(method, path)
	return slices.ContainsFunc(r.Overrides, func(o config.Override) bool {
		return o.Match.Method == method && o.Match.Path == want.Path && o.Match.PathRegex == want.PathRegex
	})
}

// Name deriva do método e do path um nome de override único na rota:
// "get-api-teste" para GET /api/teste, com sufixo numérico ("-2", "-3"...)
// quando o nome já está em uso.
func Name(r config.Route, method, path string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	dash := true
	for _, c := range strings.ToLower(path) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			if dash {
				b.WriteByte('-')
			}
			b.WriteRune(c)
			dash = false
			continue
		}
		dash = true
	}
	base := b.String()
	if base == strings.ToLower(method) {
		base += "-root"
	}
	used := func(n string) bool {
		return slices.ContainsFunc(r.Overrides, func(o config.Override) bool { return o.Name == n })
	}
	name := base
	for i := 2; used(name); i++ {
		name = base + "-" + strconv.Itoa(i)
	}
	return name
}
