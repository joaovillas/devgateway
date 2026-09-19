package config

import "time"

// SchemaVersion is the highest schema version this binary knows how to read,
// both in gateway.json and in route documents.
const SchemaVersion = 1

// GatewayFile is the content of gateway.json. Every field is optional:
// whatever the file leaves out comes from the environment or from the
// built-in default.
type GatewayFile struct {
	SchemaVersion *int          `json:"schemaVersion,omitempty" yaml:"schemaVersion,omitempty"`
	Ports         *PortsFile    `json:"ports,omitempty" yaml:"ports,omitempty"`
	Seed          *uint64       `json:"seed,omitempty" yaml:"seed,omitempty"`
	History       *HistoryFile  `json:"history,omitempty" yaml:"history,omitempty"`
	Capture       *CaptureFile  `json:"capture,omitempty" yaml:"capture,omitempty"`
	RoutesDir     *string       `json:"routesDir,omitempty" yaml:"routesDir,omitempty"`
	Learning      *LearningFile `json:"learning,omitempty" yaml:"learning,omitempty"`
}

type LearningFile struct {
	// Enabled turns on learning mode, which records every new endpoint as a
	// disabled override in the route document.
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

type PortsFile struct {
	Traffic *int `json:"traffic,omitempty" yaml:"traffic,omitempty"`
	Admin   *int `json:"admin,omitempty" yaml:"admin,omitempty"`
}

type HistoryFile struct {
	// Backend is memory, ndjson or sqlite.
	Backend *string `json:"backend,omitempty" yaml:"backend,omitempty"`
	// Path is the file used by the ndjson and sqlite backends.
	Path *string `json:"path,omitempty" yaml:"path,omitempty"`
	// Capacity is the number of exchanges the memory backend keeps.
	Capacity *int `json:"capacity,omitempty" yaml:"capacity,omitempty"`
	// Record turns on the recording of exchanges.
	Record *bool `json:"record,omitempty" yaml:"record,omitempty"`
	// Expose turns on reading the history through the API.
	Expose *bool `json:"expose,omitempty" yaml:"expose,omitempty"`
}

type CaptureFile struct {
	// MaxBodyBytes is the size above which bodies are truncated.
	MaxBodyBytes *int `json:"maxBodyBytes,omitempty" yaml:"maxBodyBytes,omitempty"`
}

// Route is a route document under routes/.
type Route struct {
	SchemaVersion int        `json:"schemaVersion" yaml:"schemaVersion"`
	Name          string     `json:"name" yaml:"name"`
	Upstream      string     `json:"upstream,omitempty" yaml:"upstream,omitempty"`
	Match         RouteMatch `json:"match" yaml:"match"`
	// StripPrefix drops the fixed part of the path pattern before forwarding.
	StripPrefix bool `json:"stripPrefix,omitempty" yaml:"stripPrefix,omitempty"`
	// RewriteHost replaces the original Host with the upstream's host. Without
	// it, the upstream sees the Host exactly as the client sent it.
	RewriteHost bool `json:"rewriteHost,omitempty" yaml:"rewriteHost,omitempty"`
	// Timeout is the upstream response deadline (504 once it is exceeded).
	Timeout   *Duration  `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Overrides []Override `json:"overrides,omitempty" yaml:"overrides,omitempty"`
}

// RouteMatch defines how the route matches. At least one of the two is required.
type RouteMatch struct {
	Host string `json:"host,omitempty" yaml:"host,omitempty"`
	// Path is exact ("/health") or a suffix wildcard ("/api/payments/*").
	Path string `json:"path,omitempty" yaml:"path,omitempty"`
}

// Override intercepts part of a route's traffic.
type Override struct {
	Name string `json:"name" yaml:"name"`
	// On is the document's enabled field: false turns the override off, and
	// leaving it out means on. Read it through the Enabled method.
	On    *bool         `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Match OverrideMatch `json:"match" yaml:"match"`
	// Respond is the synthesized response. Without it, the override only
	// delays or drops.
	Respond *Respond `json:"respond,omitempty" yaml:"respond,omitempty"`
	// Probability is the fraction of the selected requests where the override
	// applies. Leaving it out means 1.0.
	Probability *float64 `json:"probability,omitempty" yaml:"probability,omitempty"`
	Latency     *Latency `json:"latency,omitempty" yaml:"latency,omitempty"`
	Drop        bool     `json:"drop,omitempty" yaml:"drop,omitempty"`
	// TTL is the lifetime counted from when the override was recorded.
	TTL *Duration `json:"ttl,omitempty" yaml:"ttl,omitempty"`
	// MaxApplications is how many times the override may apply before it expires.
	MaxApplications *int `json:"maxApplications,omitempty" yaml:"maxApplications,omitempty"`
	// Source records the originating exchange of a learned or derived override.
	Source *OverrideSource `json:"source,omitempty" yaml:"source,omitempty"`
}

// Enabled says whether the override takes part in selection. A missing field
// means on.
func (o Override) Enabled() bool { return o.On == nil || *o.On }

const (
	// SourceLearned marks an override recorded by learning mode.
	SourceLearned = "learned"
	// SourceDerived marks an override derived from an exchange in the history.
	SourceDerived = "derived"
)

// OverrideSource is the origin of an override created from an exchange.
type OverrideSource struct {
	// Kind is learned or derived.
	Kind string `json:"kind" yaml:"kind"`
	// Exchange is the identifier of the originating exchange. It is left out
	// when the exchange was not recorded in the history (learning with
	// recording turned off), so that it never points at an exchange that does
	// not exist.
	Exchange string `json:"exchange,omitempty" yaml:"exchange,omitempty"`
	// At is when the override was created from the exchange.
	At time.Time `json:"at,omitzero" yaml:"at,omitempty"`
	// BodyIncomplete reports that the observed body was truncated on capture.
	BodyIncomplete bool `json:"bodyIncomplete,omitempty" yaml:"bodyIncomplete,omitempty"`
}

// OverrideMatch selects requests. Every criterion that is declared has to match.
type OverrideMatch struct {
	// Path is exact, with segment parameters (/zip/:id/json) or a suffix
	// wildcard. Mutually exclusive with PathRegex.
	Path      string             `json:"path,omitempty" yaml:"path,omitempty"`
	PathRegex string             `json:"pathRegex,omitempty" yaml:"pathRegex,omitempty"`
	Method    string             `json:"method,omitempty" yaml:"method,omitempty"`
	Headers   map[string]Matcher `json:"headers,omitempty" yaml:"headers,omitempty"`
	Query     map[string]Matcher `json:"query,omitempty" yaml:"query,omitempty"`
	Body      *Matcher           `json:"body,omitempty" yaml:"body,omitempty"`
}

// Respond is the declared response. Body is text or a structure serialized as
// JSON. Each header carries a single value or, when it repeats in the
// response (as several Set-Cookie do), a list of values.
type Respond struct {
	Status  int                     `json:"status,omitempty" yaml:"status,omitempty"`
	Headers map[string]HeaderValues `json:"headers,omitempty" yaml:"headers,omitempty"`
	Body    any                     `json:"body,omitempty" yaml:"body,omitempty"`
}
