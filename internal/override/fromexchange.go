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

	"github.com/gamerjp64/devgateway/internal/config"
	"github.com/gamerjp64/devgateway/internal/exchange"
)

// hopByHop are the headers that apply to a single connection and that HTTP
// forbids forwarding.
var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// excludedHeaders are left out of the pre-filled response: Date and
// Content-Length are recomputed for every response, and X-Gateway is the
// gateway's own header, which did not come from the upstream and is rebuilt
// on every intervention.
var excludedHeaders = []string{"Date", "Content-Length", "X-Gateway"}

// FromExchange builds an override from an exchange answered by the upstream:
// the request's exact path and method as the criteria, and the observed
// response — status, headers and body — as the declared response. The only
// headers left out are Date, Content-Length, the gateway's own X-Gateway and
// the hop-by-hop ones, including those named in Connection. A repeated
// header, such as several Set-Cookie, keeps all of its values, in the
// observed order. A JSON body becomes a structure; anything else becomes
// text. source records kind, the exchange (when e.ID is not empty) and the
// instant at, and whether the body was truncated during capture. The name is
// up to whoever writes the override (see Name).
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

// ExactMatch is the criterion that selects exactly the given method and path.
// In a path pattern, * is a wildcard and a segment starting with : is a
// parameter; a path that contains a literal *, has a segment starting with :
// or does not start with / cannot be written as an exact path and becomes an
// anchored regular expression that matches only itself.
func ExactMatch(method, path string) config.OverrideMatch {
	if writable(path) {
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

// responseBody returns the body as a structure when the content type is JSON
// and the body, complete, is valid JSON whose numbers fit into the structure
// without loss; otherwise as text, byte for byte as observed.
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

// decodeJSON parses the body preserving its numbers: an integer that fits in
// int64 or uint64 stays an integer, and the rest become float64. If some
// number is not representable without loss (an integer larger than uint64, a
// decimal with more digits than a float64 holds), the body is not accepted as
// a structure, so that the pre-filled response does not differ from the
// observed one.
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

// number converts the JSON literal s without loss, or reports that it cannot.
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
	// The float64 reproduces the literal when its shortest representation has
	// the same exact value as the observed text.
	want, ok1 := new(big.Rat).SetString(s)
	got, ok2 := new(big.Rat).SetString(strconv.FormatFloat(f, 'g', -1, 64))
	if !ok1 || !ok2 || want.Cmp(got) != 0 {
		return nil, false
	}
	return f, true
}

// Name derives from the method and the path an override name that is unique
// within the route: "get-api-test" for GET /api/test and "get-zip-id-json"
// for the generalized path /zip/:id/json, with a numeric suffix ("-2",
// "-3"...) when the name is already taken.
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
