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

// PathPattern is an exact path, a path with segment parameters
// ("/zip/:id/json") or a suffix wildcard, already broken apart.
type PathPattern struct {
	Raw      string
	Prefix   string // fixed part of a wildcard: "/api" in "/api/*"
	Wildcard bool
	// Segs breaks the path (or the fixed part of the wildcard) into segments
	// when it has segment parameters; nil for a path without parameters.
	Segs []PathSegment
}

// PathSegment is one segment of a path with parameters: a literal, which
// matches only itself, or a parameter (Param not empty), which matches
// exactly one non-empty segment.
type PathSegment struct {
	Literal string
	Param   string
}

// ParamPrefix marks a segment parameter: ":id" in "/zip/:id/json".
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

// HasParams reports whether the path has segment parameters.
func (p PathPattern) HasParams() bool { return p.Segs != nil }

// Literals counts the literal segments of a path with parameters: among paths
// with parameters, the one with more literals is the more specific.
func (p PathPattern) Literals() int {
	n := 0
	for _, s := range p.Segs {
		if s.Param == "" {
			n++
		}
	}
	return n
}

// Match reports whether the path matches. "/api/*" matches "/api" and
// everything under "/api/"; "/zip/:id/json" matches "/zip/40415345/json",
// but neither "/zip//json" nor "/zip/1/extra/json".
func (p PathPattern) Match(path string) bool {
	if p.Segs != nil {
		return p.matchSegments(path)
	}
	if !p.Wildcard {
		return path == p.Raw
	}
	return path == p.Prefix || strings.HasPrefix(path, p.Prefix+"/")
}

// matchSegments matches segment by segment. With a wildcard, the segments
// beyond the fixed part are free, as in "/api/*".
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

// Strip drops the fixed part of the wildcard, keeping at least "/".
func (p PathPattern) Strip(path string) string {
	rest := strings.TrimPrefix(path, p.Prefix)
	if rest == "" || rest[0] != '/' {
		rest = "/" + rest
	}
	return rest
}

// rank orders patterns: exact before wildcard, long wildcard before short one.
func (p PathPattern) rank() (int, int) {
	if !p.Wildcard {
		return 2, len(p.Raw)
	}
	return 1, len(p.Prefix)
}

// CompiledMatcher is a Matcher with its regular expression and JSON already
// prepared.
type CompiledMatcher struct {
	Matcher
	re   *regexp.Regexp
	json any
}

func compileMatcher(m Matcher) (CompiledMatcher, error) {
	c := CompiledMatcher{Matcher: m}
	if m.Regex != nil {
		c.re = regexp.MustCompile(*m.Regex) // already validated
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

// MatchString applies the operator to the value observed in the request.
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

// normalizeJSON brings a value that came from YAML or JSON into the shape
// encoding/json produces (numbers as float64), so comparisons do not depend
// on where the value came from.
func normalizeJSON(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("value cannot be represented in JSON: %v", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a) // encoding/json sorts map keys
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

// CompiledRoute is a validated route, with its upstream and patterns parsed.
type CompiledRoute struct {
	Doc      Route
	File     string
	Upstream *url.URL // nil when the route declares no upstream
	Path     *PathPattern
	// Overrides in precedence order: the first one that matches is applied.
	Overrides []*CompiledOverride

	index *nodeIndex
}

func (r *CompiledRoute) Name() string { return r.Doc.Name }

// Pattern describes how the route matches, for messages and listings.
func (r *CompiledRoute) Pattern() string {
	switch {
	case r.Doc.Match.Host != "" && r.Path != nil:
		return r.Doc.Match.Host + r.Path.Raw
	case r.Doc.Match.Host != "":
		return r.Doc.Match.Host
	}
	return r.Path.Raw
}

// Override returns the override with the given name, or nil.
func (r *CompiledRoute) Override(name string) *CompiledOverride {
	for _, o := range r.Overrides {
		if o.Doc.Name == name {
			return o
		}
	}
	return nil
}

// CompiledOverride is a validated override, with its criteria and response
// ready to use.
type CompiledOverride struct {
	Doc       Override
	Route     string
	Order     int // position in the document, the last tie-breaker
	Path      *PathPattern
	PathRegex *regexp.Regexp
	Headers   map[string]CompiledMatcher // canonical keys
	Query     map[string]CompiledMatcher
	Body      *CompiledMatcher
	// Pre-serialized response; ContentType is empty when nothing can be inferred.
	RespondBody        []byte
	RespondContentType string
}

// ID identifies the override globally: route/override.
func (o *CompiledOverride) ID() string { return o.Route + "/" + o.Doc.Name }

// Criteria counts the criteria beyond the path, to break specificity ties.
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

// pathRank orders path criteria from the most to the least specific: exact;
// with segment parameters, broken by the number of literal segments; regular
// expression; wildcard, from the longest to the shortest. The spec puts the
// segment parameter between the exact path and the wildcard and says nothing
// about where the regular expression goes; it sits after the segment
// parameter, whose structure shows how narrow it is, and before the wildcard,
// which is usually broader.
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
		c.Upstream, _ = url.Parse(r.Upstream) // already validated
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
				is = append(is, issuef(base+".respond.body", "body cannot be represented in JSON: %v", err))
			}
			c.RespondBody = js
			c.RespondContentType = "application/json"
		}
	}
	return c, is
}

// hostRank: a route with a host comes before a route without one.
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

// sortRoutes orders routes from the most to the least specific.
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
