package override

import (
	"slices"
	"strconv"
	"strings"

	"github.com/gamerjp64/gateway/internal/config"
)

// Generalize troca por parâmetros de segmento (":id", ":id2"...) os
// segmentos do path que parecem identificar um registro — só dígitos, UUID,
// ou alfanumérico (hexadecimal inclusive) com dígitos e ao menos 8
// caracteres — e mantém os demais literais: "/viacep/40415345/json" vira
// "/viacep/:id/json", e "me", "json" ou "charge" nunca viram parâmetro. Um
// path que não pode ser escrito como padrão (sem / inicial, com * ou com
// segmento começado por :) volta intacto.
func Generalize(path string) string {
	if !writable(path) {
		return path
	}
	segs := strings.Split(path[1:], "/")
	n := 0
	for i, s := range segs {
		if !isIdentifier(s) {
			continue
		}
		n++
		segs[i] = config.ParamPrefix + "id"
		if n > 1 {
			segs[i] += strconv.Itoa(n)
		}
	}
	if n == 0 {
		return path
	}
	return "/" + strings.Join(segs, "/")
}

// writable informa se o path pode ser escrito literalmente como padrão de
// path: no padrão, * é curinga e um segmento começado por : é parâmetro.
func writable(path string) bool {
	return strings.HasPrefix(path, "/") && !strings.Contains(path, "*") && !strings.Contains(path, "/"+config.ParamPrefix)
}

// isIdentifier aplica a heurística do aprendizado a um segmento. Ela é
// conservadora de propósito: um falso negativo custa só uma regra a mais,
// que o usuário generaliza à mão.
func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	if isUUID(s) {
		return true
	}
	digits, alnum := 0, true
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		default:
			alnum = false
		}
	}
	return alnum && (digits == len(s) || digits > 0 && len(s) >= 8)
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

// LearnMatch é o critério que o aprendizado grava para o método e o path
// dados: o path generalizado, quando algum segmento vira parâmetro, ou o
// critério exato de ExactMatch.
func LearnMatch(method, path string) config.OverrideMatch {
	if g := Generalize(path); g != path {
		return config.OverrideMatch{Path: g, Method: method}
	}
	return ExactMatch(method, path)
}

// Known informa se a rota já tem um override, ligado ou desligado, do mesmo
// método que torna o endpoint conhecido: com o critério que o aprendizado
// gravaria (LearnMatch), com o critério exato de ExactMatch, ou com um path
// exato ou de parâmetros de segmento que casa com o path. Curingas,
// expressões regulares livres e overrides sem método não contam, para que
// endpoints sob eles também sejam aprendidos.
func Known(r config.Route, method, path string) bool {
	learn, exact := LearnMatch(method, path), ExactMatch(method, path)
	return slices.ContainsFunc(r.Overrides, func(o config.Override) bool {
		m := o.Match
		switch {
		case m.Method != method:
			return false
		case m.Path == learn.Path && m.PathRegex == learn.PathRegex:
			return true
		case m.PathRegex != "":
			return m.PathRegex == exact.PathRegex
		}
		p := config.ParsePathPattern(m.Path)
		return !p.Wildcard && p.Match(path)
	})
}

// Absorb acrescenta o override o, gravado pelo aprendizado com um path
// generalizado, à lista, tirando dela os overrides aprendidos de path exato,
// do mesmo método, cujo path generaliza para o mesmo: o fica no lugar do
// primeiro deles (ou no fim, se nenhum é coberto), e a regra generalizada
// substitui as exatas em vez de duplicá-las. Um exato cujo segmento não é
// identificador ("/users/me" diante de "/users/:id") fica, porque é outro
// endpoint. Devolve a nova lista, sem alterar a recebida, e a posição de o
// nela. Overrides criados pelo usuário ou derivados nunca são retirados, nem
// um aprendido que o usuário já tenha ligado ou restringido com outros
// critérios: esse deixou de ser o ponto de partida intocado e continua
// valendo, com precedência sobre o generalizado.
func Absorb(list []config.Override, o config.Override) ([]config.Override, int) {
	out := make([]config.Override, 0, len(list)+1)
	at := -1
	for _, c := range list {
		if covered(c, o.Match) {
			if at < 0 {
				at = len(out)
				out = append(out, o)
			}
			continue
		}
		out = append(out, c)
	}
	if at < 0 {
		at = len(out)
		out = append(out, o)
	}
	return out, at
}

func covered(c config.Override, g config.OverrideMatch) bool {
	m := c.Match
	if c.Source == nil || c.Source.Kind != config.SourceLearned || c.Enabled() ||
		m.Method != g.Method || m.PathRegex != "" || len(m.Headers) > 0 || len(m.Query) > 0 || m.Body != nil {
		return false
	}
	return g.Path != "" && m.Path != g.Path && Generalize(m.Path) == g.Path
}
