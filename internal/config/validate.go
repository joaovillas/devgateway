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

// validateRoute applies the rules of a route document, reporting each problem
// against the field responsible for it.
func validateRoute(r Route) []issue {
	var is []issue
	add := func(field, format string, args ...any) { is = append(is, issuef(field, format, args...)) }

	switch {
	case r.SchemaVersion == 0:
		add("schemaVersion", "required (current version: %d)", SchemaVersion)
	case r.SchemaVersion < 0:
		add("schemaVersion", "invalid schema version %d", r.SchemaVersion)
	case r.SchemaVersion > SchemaVersion:
		is = append(is, schemaIssue(r.SchemaVersion))
	}

	if !routeNameRe.MatchString(r.Name) {
		add("name", "required, with lowercase letters, digits, - or _ (got %q)", r.Name)
	}

	if r.Upstream != "" {
		if msg := checkUpstream(r.Upstream); msg != "" {
			add("upstream", "%s (got %q)", msg, r.Upstream)
		}
	}

	if r.Match.Host == "" && r.Match.Path == "" {
		add("match", "declare host, path or both")
	}
	if r.Match.Host != "" && strings.ContainsAny(r.Match.Host, "/ *") {
		add("match.host", "invalid host %q", r.Match.Host)
	}
	if r.Match.Path != "" {
		if msg := checkPathPattern(r.Match.Path, false); msg != "" {
			add("match.path", "%s", msg)
		}
	}
	if r.StripPrefix && !strings.HasSuffix(r.Match.Path, "*") {
		add("stripPrefix", "only applies to a wildcard path pattern, such as /api/*")
	}
	if r.Timeout != nil && *r.Timeout <= 0 {
		add("timeout", "must be greater than zero")
	}

	names := map[string]int{}
	for i, o := range r.Overrides {
		base := fmt.Sprintf("overrides[%d]", i)
		if o.Name == "" {
			add(base+".name", "required")
		} else if j, dup := names[o.Name]; dup {
			add(base+".name", "duplicate name %q (already used by overrides[%d])", o.Name, j)
		} else {
			names[o.Name] = i
		}
		is = append(is, validateOverride(base, o)...)
	}
	return is
}

// ValidateOverride applies to a standalone override the override rules of the
// route document (the ones that do not depend on the other overrides, such as
// name uniqueness). file names the document in the errors.
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
		add("match", "declare path or pathRegex")
	case m.Path != "" && m.PathRegex != "":
		add("match.pathRegex", "use path or pathRegex, not both")
	case m.Path != "":
		if msg := checkPathPattern(m.Path, true); msg != "" {
			add("match.path", "%s", msg)
		}
	default:
		if _, err := regexp.Compile(m.PathRegex); err != nil {
			add("match.pathRegex", "invalid regular expression: %v", err)
		}
	}
	if m.Method != "" && !methodRe.MatchString(m.Method) {
		add("match.method", "method must be uppercase, such as POST (got %q)", m.Method)
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

	if o.Respond == nil && o.Latency == nil && !o.Drop.Declared() {
		add("", "declare at least one of respond, latency or drop")
	}
	// Each effect carries its own frequency; probability is the legacy field
	// that still works as the default of the effects that declare none. Every
	// one of them is pinned to the field that holds it.
	checkChance := func(field string, c *float64) {
		if c != nil && (*c < 0 || *c > 1) {
			add(field, "must be between 0.0 and 1.0 (got %v)", *c)
		}
	}
	checkChance("probability", o.Probability)
	checkChance("drop.chance", o.Drop.Chance)
	if r := o.Respond; r != nil {
		if r.Status != 0 && (r.Status < 100 || r.Status > 599) {
			add("respond.status", "status must be between 100 and 599 (got %d)", r.Status)
		}
		for _, k := range slices.Sorted(maps.Keys(r.Headers)) {
			if !validHeaderName(k) {
				add("respond.headers."+k, "invalid header name")
			}
			if len(r.Headers[k]) == 0 {
				add("respond.headers."+k, "declare at least one value")
			}
		}
		checkChance("respond.chance", r.Chance)
	}
	if l := o.Latency; l != nil {
		checkChance("latency.chance", l.Chance)
		switch {
		case l.Fixed != nil:
			if *l.Fixed < 0 {
				add("latency", "cannot be negative")
			}
		case l.Min == nil || l.Max == nil:
			add("latency", "a range needs both min and max")
		case *l.Min < 0:
			add("latency.min", "cannot be negative")
		case *l.Min > *l.Max:
			add("latency.min", "minimum %s is greater than the maximum %s", l.Min, l.Max)
		}
	}
	if o.TTL != nil && *o.TTL <= 0 {
		add("ttl", "must be greater than zero")
	}
	if n := o.MaxApplications; n != nil && *n < 1 {
		add("maxApplications", "must be at least 1 (got %d)", *n)
	}
	if s := o.Source; s != nil {
		switch s.Kind {
		case SourceLearned, SourceDerived:
		case "":
			add("source.kind", "required; use %s or %s", SourceLearned, SourceDerived)
		default:
			add("source.kind", "unknown source %q; use %s or %s", s.Kind, SourceLearned, SourceDerived)
		}
		// A derived override always starts from an exchange in the history. A
		// learned one with recording turned off has no recorded exchange to
		// point at.
		if s.Exchange == "" && s.Kind != SourceLearned {
			add("source.exchange", "required: the identifier of the originating exchange")
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
		return []issue{issuef(field, "declare exactly one operator among equals, regex, json and contains")}
	}
	if m.Regex != nil {
		if _, err := regexp.Compile(*m.Regex); err != nil {
			return []issue{issuef(field+".regex", "invalid regular expression: %v", err)}
		}
	}
	return nil
}

func checkUpstream(s string) string {
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return "is not a valid URL"
	case u.Scheme != "http" && u.Scheme != "https":
		return "the upstream URL must use http or https"
	case u.Host == "":
		return "the upstream URL has no host"
	case u.RawQuery != "" || u.Fragment != "":
		return "the upstream URL cannot carry a query or a fragment"
	}
	return ""
}

// paramNameRe is the shape of a segment parameter name.
var paramNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkPathPattern accepts an exact path ("/health") or a suffix wildcard
// ("/api/*"); with params, it also accepts segment parameters
// ("/zip/:id/json"), which only an override path takes.
func checkPathPattern(p string, params bool) string {
	if !strings.HasPrefix(p, "/") {
		return fmt.Sprintf("the pattern must start with / (got %q)", p)
	}
	seen := map[string]bool{}
	for seg := range strings.SplitSeq(p[1:], "/") {
		name, ok := strings.CutPrefix(seg, ParamPrefix)
		switch {
		case !ok:
		case !params:
			return fmt.Sprintf("segment parameters such as :id are only accepted in an override path (got %q)", p)
		case strings.Contains(name, "*"):
			return fmt.Sprintf("a segment cannot be a parameter and a wildcard at the same time (got %q)", p)
		case !paramNameRe.MatchString(name):
			return fmt.Sprintf("invalid segment parameter %q: use : followed by letters, digits or _, not starting with a digit, such as :id (got %q)", seg, p)
		case seen[name]:
			return fmt.Sprintf("duplicate segment parameter :%s in the path (got %q)", name, p)
		default:
			seen[name] = true
		}
	}
	star := strings.Index(p, "*")
	if star >= 0 && (star != len(p)-1 || !strings.HasSuffix(p, "/*")) {
		return fmt.Sprintf("the wildcard is only accepted at the end, after /, such as /api/* (got %q)", p)
	}
	return ""
}

func validHeaderName(k string) bool {
	return k != "" && http.CanonicalHeaderKey(k) != "" && !strings.ContainsAny(k, " \t\r\n:")
}
