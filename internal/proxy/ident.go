package proxy

import "strings"

// HeaderGateway is the only header of its own that the gateway adds to
// traffic, both on the request to the upstream and on the response to the
// client.
const HeaderGateway = "X-Gateway"

// Ident is the content of the X-Gateway header: the matched route and, when
// an override steps in, the override responsible and the kind of
// intervention.
type Ident struct {
	Route        string // name of the matched route
	Override     string // "route/override", when an override steps in
	Intervention string // kind of intervention, such as "synthesized"
}

// String builds the value in the form "route=x; override=x/y;
// intervention=synthesized", omitting the empty parts. With no part at all
// (a response with no matched route) the value is empty and the header is
// still present, marking the response as produced by the gateway.
func (id Ident) String() string {
	parts := make([]string, 0, 3)
	for _, p := range [...]struct{ key, val string }{
		{"route", id.Route},
		{"override", id.Override},
		{"intervention", id.Intervention},
	} {
		if p.val != "" {
			parts = append(parts, p.key+"="+p.val)
		}
	}
	return strings.Join(parts, "; ")
}
