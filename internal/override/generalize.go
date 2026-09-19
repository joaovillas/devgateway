package override

import (
	"slices"
	"strconv"
	"strings"

	"github.com/gamerjp64/devgateway/internal/config"
)

// Generalize replaces with segment parameters (":id", ":id2"...) the path
// segments that look like they identify a record — all digits, a UUID, or
// alphanumeric (hexadecimal included) with digits and at least 8 characters —
// and keeps the rest literal: "/zip/40415345/json" becomes
// "/zip/:id/json", and "me", "json" or "charge" never become a parameter.
// A path that cannot be written as a pattern (no leading /, containing * or
// with a segment starting with :) comes back untouched.
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

// writable reports whether the path can be written literally as a path
// pattern: in a pattern, * is a wildcard and a segment starting with : is a
// parameter.
func writable(path string) bool {
	return strings.HasPrefix(path, "/") && !strings.Contains(path, "*") && !strings.Contains(path, "/"+config.ParamPrefix)
}

// isIdentifier applies the learning heuristic to a segment. It is
// deliberately conservative: a false negative costs only one extra rule,
// which the user generalizes by hand.
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

// LearnMatch is the criterion that learning writes for the given method and
// path: the generalized path, when some segment becomes a parameter, or
// ExactMatch's exact criterion.
func LearnMatch(method, path string) config.OverrideMatch {
	if g := Generalize(path); g != path {
		return config.OverrideMatch{Path: g, Method: method}
	}
	return ExactMatch(method, path)
}

// Known reports whether the route already has an override, enabled or not, of
// the same method that makes the endpoint known: with the criterion learning
// would write (LearnMatch), with ExactMatch's exact criterion, or with an
// exact or segment-parameter path that matches the path. Wildcards, free
// regular expressions and overrides without a method do not count, so that
// endpoints under them are learned as well.
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

// Absorb adds the override o, written by learning with a generalized path, to
// the list, dropping from it the learned exact-path overrides, of the same
// method, whose path generalizes to the same one: o takes the place of the
// first of them (or goes to the end, if none is covered), and the generalized
// rule replaces the exact ones instead of duplicating them. An exact override
// whose segment is not an identifier ("/users/me" against "/users/:id")
// stays, because it is a different endpoint. Returns the new list, leaving
// the given one unchanged, and o's position in it. Overrides created by the
// user or derived are never dropped, nor is a learned one that the user has
// already enabled or narrowed with other criteria: that one is no longer the
// untouched starting point, and it stays in effect, taking precedence over
// the generalized one.
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
