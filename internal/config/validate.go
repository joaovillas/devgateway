package config

import (
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var (
	routeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	methodRe    = regexp.MustCompile(`^[A-Z]+$`)
)

// validateRoute aplica as regras de um documento de rota, devolvendo cada
// problema com o caminho do campo responsável.
func validateRoute(r Route) []issue {
	var is []issue
	add := func(field, format string, args ...any) { is = append(is, issuef(field, format, args...)) }

	switch {
	case r.SchemaVersion == 0:
		add("schemaVersion", "obrigatório (versão atual: %d)", SchemaVersion)
	case r.SchemaVersion < 0:
		add("schemaVersion", "versão de schema %d inválida", r.SchemaVersion)
	case r.SchemaVersion > SchemaVersion:
		is = append(is, schemaIssue(r.SchemaVersion))
	}

	if !routeNameRe.MatchString(r.Name) {
		add("name", "obrigatório, com letras minúsculas, dígitos, - ou _ (recebido %q)", r.Name)
	}

	if r.Upstream != "" {
		if msg := checkUpstream(r.Upstream); msg != "" {
			add("upstream", "%s (recebido %q)", msg, r.Upstream)
		}
	}

	if r.Match.Host == "" && r.Match.Path == "" {
		add("match", "declare host, path ou ambos")
	}
	if r.Match.Host != "" && strings.ContainsAny(r.Match.Host, "/ *") {
		add("match.host", "host inválido %q", r.Match.Host)
	}
	if r.Match.Path != "" {
		if msg := checkPathPattern(r.Match.Path); msg != "" {
			add("match.path", "%s", msg)
		}
	}
	if r.StripPrefix && !strings.HasSuffix(r.Match.Path, "*") {
		add("stripPrefix", "só se aplica a um padrão de path com curinga, como /api/*")
	}
	if r.Timeout != nil && *r.Timeout <= 0 {
		add("timeout", "deve ser maior que zero")
	}

	names := map[string]int{}
	for i, o := range r.Overrides {
		base := fmt.Sprintf("overrides[%d]", i)
		if o.Name == "" {
			add(base+".name", "obrigatório")
		} else if j, dup := names[o.Name]; dup {
			add(base+".name", "nome %q repetido (já usado em overrides[%d])", o.Name, j)
		} else {
			names[o.Name] = i
		}
		is = append(is, validateOverride(base, o)...)
	}
	return is
}

// ValidateOverride aplica a um override isolado as regras de override do
// documento de rota (as que não dependem dos demais overrides, como a
// unicidade do nome). file nomeia o documento nos erros.
func ValidateOverride(file string, o Override) error {
	is := validateOverride("override", o)
	if len(is) == 0 {
		return nil
	}
	x := &nodeIndex{file: file}
	return x.errors(is)
}

func validateOverride(base string, o Override) []issue {
	var is []issue
	add := func(field, format string, args ...any) {
		is = append(is, issuef(joinField(base, field), format, args...))
	}

	m := o.Match
	switch {
	case m.Path == "" && m.PathRegex == "":
		add("match", "declare path ou pathRegex")
	case m.Path != "" && m.PathRegex != "":
		add("match.pathRegex", "use path ou pathRegex, não os dois")
	case m.Path != "":
		if msg := checkPathPattern(m.Path); msg != "" {
			add("match.path", "%s", msg)
		}
	default:
		if _, err := regexp.Compile(m.PathRegex); err != nil {
			add("match.pathRegex", "expressão regular inválida: %v", err)
		}
	}
	if m.Method != "" && !methodRe.MatchString(m.Method) {
		add("match.method", "método deve estar em maiúsculas, como POST (recebido %q)", m.Method)
	}
	for _, k := range slices.Sorted(maps.Keys(m.Headers)) {
		is = append(is, validateMatcher(base+".match.headers."+k, m.Headers[k])...)
	}
	for _, k := range slices.Sorted(maps.Keys(m.Query)) {
		is = append(is, validateMatcher(base+".match.query."+k, m.Query[k])...)
	}
	if m.Body != nil {
		is = append(is, validateMatcher(base+".match.body", *m.Body)...)
	}

	if o.Respond == nil && o.Latency == nil && !o.Drop {
		add("", "declare ao menos respond, latency ou drop")
	}
	if r := o.Respond; r != nil {
		if r.Status != 0 && (r.Status < 100 || r.Status > 599) {
			add("respond.status", "status deve estar entre 100 e 599 (recebido %d)", r.Status)
		}
		for _, k := range slices.Sorted(maps.Keys(r.Headers)) {
			if !validHeaderName(k) {
				add("respond.headers."+k, "nome de cabeçalho inválido")
			}
			if len(r.Headers[k]) == 0 {
				add("respond.headers."+k, "declare ao menos um valor")
			}
		}
	}
	if p := o.Probability; p != nil && (*p < 0 || *p > 1) {
		add("probability", "deve estar entre 0.0 e 1.0 (recebido %v)", *p)
	}
	if l := o.Latency; l != nil {
		switch {
		case l.Fixed != nil:
			if *l.Fixed < 0 {
				add("latency", "não pode ser negativa")
			}
		case l.Min == nil || l.Max == nil:
			add("latency", "o intervalo exige min e max")
		case *l.Min < 0:
			add("latency.min", "não pode ser negativa")
		case *l.Min > *l.Max:
			add("latency.min", "mínimo %s maior que o máximo %s", l.Min, l.Max)
		}
	}
	if o.TTL != nil && *o.TTL <= 0 {
		add("ttl", "deve ser maior que zero")
	}
	if n := o.MaxApplications; n != nil && *n < 1 {
		add("maxApplications", "deve ser ao menos 1 (recebido %d)", *n)
	}
	if s := o.Source; s != nil {
		switch s.Kind {
		case SourceLearned, SourceDerived:
		case "":
			add("source.kind", "obrigatório; use %s ou %s", SourceLearned, SourceDerived)
		default:
			add("source.kind", "origem %q desconhecida; use %s ou %s", s.Kind, SourceLearned, SourceDerived)
		}
		// Um override derivado sempre parte de uma troca do histórico. Um
		// aprendido com o registro desligado não tem troca gravada a apontar.
		if s.Exchange == "" && s.Kind != SourceLearned {
			add("source.exchange", "obrigatório: identificador da troca de origem")
		}
	}
	return is
}

func validateMatcher(field string, m Matcher) []issue {
	n := 0
	for _, set := range []bool{m.Equals != nil, m.Regex != nil, m.JSON != nil, m.Contains != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		return []issue{issuef(field, "declare exatamente um operador entre equals, regex, json e contains")}
	}
	if m.Regex != nil {
		if _, err := regexp.Compile(*m.Regex); err != nil {
			return []issue{issuef(field+".regex", "expressão regular inválida: %v", err)}
		}
	}
	return nil
}

func checkUpstream(s string) string {
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return "não é uma URL válida"
	case u.Scheme != "http" && u.Scheme != "https":
		return "a URL do upstream deve usar http ou https"
	case u.Host == "":
		return "a URL do upstream não tem host"
	case u.RawQuery != "" || u.Fragment != "":
		return "a URL do upstream não pode ter query nem fragmento"
	}
	return ""
}

// checkPathPattern aceita path exato ("/health") ou curinga de sufixo ("/api/*").
func checkPathPattern(p string) string {
	if !strings.HasPrefix(p, "/") {
		return fmt.Sprintf("o padrão deve começar com / (recebido %q)", p)
	}
	star := strings.Index(p, "*")
	if star >= 0 && (star != len(p)-1 || !strings.HasSuffix(p, "/*")) {
		return fmt.Sprintf("o curinga só é aceito no fim, depois de /, como /api/* (recebido %q)", p)
	}
	return ""
}

func validHeaderName(k string) bool {
	return k != "" && http.CanonicalHeaderKey(k) != "" && !strings.ContainsAny(k, " \t\r\n:")
}
