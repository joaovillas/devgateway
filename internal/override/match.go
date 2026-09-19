// Package override decides whether one of the route's overrides steps into a
// request: it selects the most specific enabled override that matches (step 3
// of the request path), draws application, drop and delay from a randomness
// source of the request's own (step 4) and prepares the declared response.
package override

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/joaovillas/devgateway/internal/config"
)

// MaxBodyMatchBytes caps how much of the request body is read to evaluate a
// body criterion. A larger body matches no body criterion, and is forwarded
// to the upstream in full all the same.
const MaxBodyMatchBytes = 8 << 20

// Request is the request under evaluation. The query is parsed and the body
// is read only when some criterion needs them, and only once.
type Request struct {
	r *http.Request

	query url.Values

	bodyRead bool
	body     []byte
	// bodyOK reports that the body was read in full, within the limit.
	bodyOK bool
}

// NewRequest prepares the request for evaluation.
func NewRequest(r *http.Request) *Request { return &Request{r: r} }

// Request returns the request to use from here on. If the body was read for
// matching, it carries a body that hands back exactly the same bytes again,
// followed by whatever had not been read yet, so that the upstream receives
// it intact.
func (q *Request) Request() *http.Request {
	if !q.bodyRead || q.r.Body == nil || q.r.Body == http.NoBody {
		return q.r
	}
	r2 := *q.r
	r2.Body = &replayBody{Reader: io.MultiReader(bytes.NewReader(q.body), q.r.Body), c: q.r.Body}
	return &r2
}

// replayBody hands back the bytes already read and then the rest of the
// original body, which is the one that gets closed.
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

// readBody reads the body up to the limit. A body over the limit or a read
// failure leaves bodyOK false: no body criterion matches, and what was read
// is handed back to forwarding by Request.
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
		// Whoever forwards it will read the rest and hit the same failure.
		q.bodyOK = false
	}
	return q.body, q.bodyOK
}

// Select returns the override that holds for the request: among the enabled
// ones that select it, the most specific. The route's overrides are already
// in precedence order (exact path; segment parameters, from most literals to
// fewest; regular expression; wildcard from longest to shortest; more
// criteria; declaration order), so the first one that matches wins. Returns
// nil when none matches.
func Select(route *config.CompiledRoute, q *Request) *config.CompiledOverride {
	return SelectWhere(route, q, nil)
}

// SelectWhere is Select restricted to the overrides accepted by ok: the
// rejected ones stay out of the selection and out of precedence, like the
// disabled ones, and do not hide a less specific one that also matches. With
// ok nil, this is Select.
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

// Match reports whether the override selects the request: every declared
// criterion has to match. The body is evaluated last, because it is the only
// criterion that requires reading the request.
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

// headerValues returns the values of the header with canonical name k. Host,
// which the server strips from the headers, comes from the request's own
// field.
func headerValues(r *http.Request, k string) []string {
	if k == "Host" {
		return []string{r.Host}
	}
	return r.Header.Values(k)
}

// anyMatch matches when one of the observed values satisfies the operator. A
// missing header or parameter matches no operator.
func anyMatch(m config.CompiledMatcher, vs []string) bool {
	for _, v := range vs {
		if m.MatchString(v) {
			return true
		}
	}
	return false
}
