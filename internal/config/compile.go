package config

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// PathPattern é um path exato, um path com parâmetros de segmento
// ("/viacep/:id/json") ou um curinga de sufixo, já decomposto.
type PathPattern struct {
	Raw      string
	Prefix   string // parte fixa de um curinga: "/api" em "/api/*"
	Wildcard bool
	// Segs decompõe o path (ou a parte fixa do curinga) quando ele tem
	// parâmetros de segmento; nil num path sem parâmetros.
	Segs []PathSegment
}

// PathSegment é um segmento de um path com parâmetros: um literal, que casa
// só com ele mesmo, ou um parâmetro (Param não vazio), que casa com
// exatamente um segmento não vazio.
type PathSegment struct {
	Literal string
	Param   string
}

// ParamPrefix marca um parâmetro de segmento: ":id" em "/viacep/:id/json".
const ParamPrefix = ":"

func ParsePathPattern(p string) PathPattern {
	pp := PathPattern{Raw: p}
	base := p
	if strings.HasSuffix(p, "/*") {
		pp.Prefix, pp.Wildcard = strings.TrimSuffix(p, "/*"), true
		base = pp.Prefix
	}
	if strings.HasPrefix(base, "/") && strings.Contains(base, "/"+ParamPrefix) {
		for s := range strings.SplitSeq(base[1:], "/") {
			if name, ok := strings.CutPrefix(s, ParamPrefix); ok {
				pp.Segs = append(pp.Segs, PathSegment{Param: name})
			} else {
				pp.Segs = append(pp.Segs, PathSegment{Literal: s})
			}
		}
	}
	return pp
}

// HasParams informa se o path tem parâmetros de segmento.
func (p PathPattern) HasParams() bool { return p.Segs != nil }

// Literals conta os segmentos literais de um path com parâmetros: entre
// paths com parâmetros, o de mais literais é o mais específico.
func (p PathPattern) Literals() int {
	n := 0
	for _, s := range p.Segs {
		if s.Param == "" {
			n++
		}
	}
	return n
}

// Match informa se o path casa. "/api/*" casa com "/api" e com tudo sob
// "/api/"; "/viacep/:id/json" casa com "/viacep/40415345/json", mas não com
// "/viacep//json" nem com "/viacep/1/extra/json".
func (p PathPattern) Match(path string) bool {
	if p.Segs != nil {
		return p.matchSegments(path)
	}
	if !p.Wildcard {
		return path == p.Raw
	}
	return path == p.Prefix || strings.HasPrefix(path, p.Prefix+"/")
}

// matchSegments casa segmento a segmento. Com curinga, os segmentos além da
// parte fixa são livres, como em "/api/*".
func (p PathPattern) matchSegments(path string) bool {
	rest, ok := strings.CutPrefix(path, "/")
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	if len(parts) < len(p.Segs) || (!p.Wildcard && len(parts) != len(p.Segs)) {
		return false
	}
	for i, s := range p.Segs {
		switch {
		case s.Param != "":
			if parts[i] == "" {
				return false
			}
		case parts[i] != s.Literal:
			return false
		}
	}
	return true
}

// Strip remove a parte fixa do curinga, preservando ao menos "/".
func (p PathPattern) Strip(path string) string {
	rest := strings.TrimPrefix(path, p.Prefix)
	if rest == "" || rest[0] != '/' {
		rest = "/" + rest
	}
	return rest
}

// rank ordena padrões: exato antes de curinga, curinga longo antes de curto.
func (p PathPattern) rank() (int, int) {
	if !p.Wildcard {
		return 2, len(p.Raw)
	}
	return 1, len(p.Prefix)
}

// CompiledMatcher é um Matcher com expressão regular e JSON já preparados.
type CompiledMatcher struct {
	Matcher
	re   *regexp.Regexp
	json any
}

func compileMatcher(m Matcher) (CompiledMatcher, error) {
	c := CompiledMatcher{Matcher: m}
	if m.Regex != nil {
		c.re = regexp.MustCompile(*m.Regex) // já validado
	}
	if m.JSON != nil {
		v, err := normalizeJSON(m.JSON)
		if err != nil {
			return c, err
		}
		c.json = v
	}
	return c, nil
}

// MatchString aplica o operador ao valor observado na requisição.
func (c CompiledMatcher) MatchString(s string) bool {
	switch {
	case c.Equals != nil:
		return s == *c.Equals
	case c.re != nil:
		return c.re.MatchString(s)
	case c.Contains != nil:
		return strings.Contains(s, *c.Contains)
	case c.JSON != nil:
		var v any
		if json.Unmarshal([]byte(s), &v) != nil {
			return false
		}
		return jsonEqual(v, c.json)
	}
	return false
}

// normalizeJSON leva um valor vindo do YAML ou do JSON à forma de
// encoding/json (números como float64), para comparar sem depender da origem.
func normalizeJSON(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("valor não representável em JSON: %v", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a) // encoding/json ordena as chaves dos mapas
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

// CompiledRoute é uma rota validada, com upstream e padrões interpretados.
type CompiledRoute struct {
	Doc      Route
	File     string
	Upstream *url.URL // nil quando a rota não declara upstream
	Path     *PathPattern
	// Overrides em ordem de precedência: o primeiro que casa é o aplicado.
	Overrides []*CompiledOverride

	index *nodeIndex
}

func (r *CompiledRoute) Name() string { return r.Doc.Name }

// Pattern descreve o casamento da rota para mensagens e listagens.
func (r *CompiledRoute) Pattern() string {
	switch {
	case r.Doc.Match.Host != "" && r.Path != nil:
		return r.Doc.Match.Host + r.Path.Raw
	case r.Doc.Match.Host != "":
		return r.Doc.Match.Host
	}
	return r.Path.Raw
}

// Override devolve o override de nome dado, ou nil.
func (r *CompiledRoute) Override(name string) *CompiledOverride {
	for _, o := range r.Overrides {
		if o.Doc.Name == name {
			return o
		}
	}
	return nil
}

// CompiledOverride é um override validado, com critérios e resposta prontos.
type CompiledOverride struct {
	Doc       Override
	Route     string
	Order     int // posição no documento, último critério de desempate
	Path      *PathPattern
	PathRegex *regexp.Regexp
	Headers   map[string]CompiledMatcher // chaves canônicas
	Query     map[string]CompiledMatcher
	Body      *CompiledMatcher
	// Resposta pré-serializada; ContentType vazio quando nada se infere.
	RespondBody        []byte
	RespondContentType string
}

// ID identifica o override globalmente: rota/override.
func (o *CompiledOverride) ID() string { return o.Route + "/" + o.Doc.Name }

// Criteria conta os critérios além do path, para desempate por especificidade.
func (o *CompiledOverride) Criteria() int {
	n := len(o.Headers) + len(o.Query)
	if o.Doc.Match.Method != "" {
		n++
	}
	if o.Body != nil {
		n++
	}
	return n
}

// pathRank ordena os critérios de path do mais ao menos específico: exato;
// com parâmetros de segmento, desempatado pelo número de segmentos literais;
// expressão regular; curinga, do mais longo ao mais curto. A spec põe o
// parâmetro de segmento entre o exato e o curinga e não posiciona a expressão
// regular; ela fica depois do parâmetro de segmento, cuja estrutura mostra o
// quanto ele é estreito, e antes do curinga, que costuma ser mais largo.
func (o *CompiledOverride) pathRank() (int, int) {
	switch {
	case o.PathRegex != nil:
		return 2, 0
	case o.Path.Wildcard:
		return 1, len(o.Path.Prefix)
	case o.Path.HasParams():
		return 3, o.Path.Literals()
	}
	return 4, len(o.Path.Raw)
}

func compileRoute(file string, r Route) (*CompiledRoute, []issue) {
	c := &CompiledRoute{Doc: r, File: file}
	if r.Upstream != "" {
		c.Upstream, _ = url.Parse(r.Upstream) // já validado
	}
	if r.Match.Path != "" {
		p := ParsePathPattern(r.Match.Path)
		c.Path = &p
	}
	var is []issue
	for i, o := range r.Overrides {
		co, oi := compileOverride(r.Name, i, o)
		is = append(is, oi...)
		c.Overrides = append(c.Overrides, co)
	}
	slices.SortStableFunc(c.Overrides, func(a, b *CompiledOverride) int {
		ar, al := a.pathRank()
		br, bl := b.pathRank()
		return cmp.Or(
			cmp.Compare(br, ar),
			cmp.Compare(bl, al),
			cmp.Compare(b.Criteria(), a.Criteria()),
			cmp.Compare(a.Order, b.Order),
		)
	})
	return c, is
}

func compileOverride(route string, i int, o Override) (*CompiledOverride, []issue) {
	base := fmt.Sprintf("overrides[%d]", i)
	c := &CompiledOverride{Doc: o, Route: route, Order: i}
	var is []issue
	if o.Match.PathRegex != "" {
		c.PathRegex = regexp.MustCompile(o.Match.PathRegex)
	} else {
		p := ParsePathPattern(o.Match.Path)
		c.Path = &p
	}
	compileAll := func(field string, in map[string]Matcher, canon func(string) string) map[string]CompiledMatcher {
		if len(in) == 0 {
			return nil
		}
		out := make(map[string]CompiledMatcher, len(in))
		for k, m := range in {
			cm, err := compileMatcher(m)
			if err != nil {
				is = append(is, issuef(base+".match."+field+"."+k, "%v", err))
			}
			out[canon(k)] = cm
		}
		return out
	}
	c.Headers = compileAll("headers", o.Match.Headers, http.CanonicalHeaderKey)
	c.Query = compileAll("query", o.Match.Query, func(s string) string { return s })
	if o.Match.Body != nil {
		cm, err := compileMatcher(*o.Match.Body)
		if err != nil {
			is = append(is, issuef(base+".match.body", "%v", err))
		}
		c.Body = &cm
	}
	if r := o.Respond; r != nil {
		switch b := r.Body.(type) {
		case nil:
		case string:
			c.RespondBody = []byte(b)
		default:
			js, err := json.Marshal(b)
			if err != nil {
				is = append(is, issuef(base+".respond.body", "corpo não representável em JSON: %v", err))
			}
			c.RespondBody = js
			c.RespondContentType = "application/json"
		}
	}
	return c, is
}

// hostRank: rota com host vem antes de rota sem host.
func (r *CompiledRoute) hostRank() int {
	if r.Doc.Match.Host != "" {
		return 1
	}
	return 0
}

func (r *CompiledRoute) pathRank() (int, int) {
	if r.Path == nil {
		return 0, 0
	}
	return r.Path.rank()
}

// sortRoutes ordena da rota mais específica para a menos específica.
func sortRoutes(rs []*CompiledRoute) {
	slices.SortStableFunc(rs, func(a, b *CompiledRoute) int {
		ar, al := a.pathRank()
		br, bl := b.pathRank()
		return cmp.Or(
			cmp.Compare(b.hostRank(), a.hostRank()),
			cmp.Compare(br, ar),
			cmp.Compare(bl, al),
			strings.Compare(a.Doc.Name, b.Doc.Name),
		)
	})
}
