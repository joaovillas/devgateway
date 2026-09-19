## Why

In a development environment every service comes up on a different port and the client has to know all of them. Worse: there is no way to reproduce on demand the conditions that break the system in production — a slow upstream, an intermittent error, a dropped connection — so retry, timeout and circuit breaker code gets written without ever being exercised.

The existing tools solve half of this. MockServer has the complete chaos engine (error probability, TTL, latency waterfall) but spreads the feature across 21 screens and runs on the JVM. Smocker is light and calls itself an API gateway, yet it has no percentage error rate — only hand-written Lua scripts, one mock at a time. Neither shows the topology of the call: both hand you a log list, not the `client → route → upstream` path.

And both separate "mock" from "chaos" as distinct mechanisms, when in practice they are the same gesture with a different probability: forcing a response is injecting chaos with probability `1.0`.

## What Changes

- New product: a self-hosted HTTP gateway for development environments, shipped as a **single Go binary** with no external dependencies.
- **Reverse routing** of N upstream services behind a single port, by path prefix (wildcard included) or by host. **The gateway is transparent**: method, path, query, body, headers and `Host` go through as they arrived, and it only adds the `X-Forwarded-*` headers plus a single header of its own, `X-Gateway`.
- **Passthrough by default, surgical override on top.** The route forwards everything; `overrides` intercept specific paths. A single concept replaces the split between mock and chaos: the override declares `respond`, and the fields `probability`, `latency`, `drop` and `ttl` decide whether it applies always, sometimes or for a limited time. Probability `1.0` is a forced response; `0.3` is chaos; whatever is not drawn goes on to the upstream.
- **Precedence by specificity**: a more specific override beats a less specific one, which beats the route's wildcard.
- **Deterministic seed** to make the probabilistic behavior reproducible across runs.
- **Configuration in separate documents**, with English keys: `gateway.json` for the process configuration and one YAML document per route under `routes/`, merged into a snapshot on load. Writing through the API rewrites only the document of the affected route.
- **Traffic capture** with a waterfall that separates the upstream's real time from the time the gateway injected.
- **Pluggable log storage**, selected by environment variable: memory (the default), an NDJSON file or a local SQLite database.
- **Configurable log exposure**, with reads of a single entry by identifier and cursor navigation, alongside the paginated listing.
- **Web interface** embedded in the binary (`go:embed`), with a navigable topology map and the override's controls right on the route.
- **Full runtime reconfiguration**: routes, overrides and the whole process configuration — ports and history backend included — change without restarting the process.
- **Learning mode**: turned on, every new endpoint that goes through the gateway is saved to the route document as a disabled override, already filled in with the real response observed, ready to take chaos or a custom response. Turned off, the gateway applies only what is configured.
- **Parity across file, API and interface**: everything configurable in `gateway.json` and in the route documents is also configurable through the admin API and through the interface, which is nothing more than a client of that API.

Explicitly out of scope for this change: SLOs, contract testing, load testing, gRPC, AsyncAPI, LLM mocking, live breakpoints and clustering. These are the axes that bloated MockServer and they do not serve the problem above.

Nothing breaks: the project has no code and no consumers.

## Capabilities

### New Capabilities

- `gateway-routing`: receiving requests on a single port and forwarding them to the right upstream by path wildcard or host, including header enrichment and upstream failure handling.
- `route-overrides`: selective interception of paths within a route, with selection criteria, a declared response, an application probability, latency, connection drops, expiry by time and by count, on/off, determinism by seed, derivation from a captured exchange, and automatic endpoint learning.
- `traffic-capture`: recording the HTTP exchanges that go through the gateway, with time broken down into real versus injected latency, pluggable storage, and querying by listing, identifier or cursor.
- `gateway-config`: `gateway.json` and the route documents under `routes/` as the source of truth, with validation on load, merging into a snapshot, hot reconfiguration of everything (ports and history backend included), and an admin API that reads and writes those documents and the process configuration.
- `control-panel`: a web interface served by the binary itself, with a navigable topology map, inspection of the captured traffic, and editing of routes and overrides.

### Modified Capabilities

None. The project has no specs yet.

## Impact

- **Code**: greenfield project. It creates the Go module (`cmd/`, `internal/`) and the React + TypeScript frontend embedded via `go:embed`.
- **Dependencies**: Go 1.26 and Node to build the frontend at build time. At runtime, none: the SQLite driver MUST be a pure Go implementation, without CGO, to keep the binary static.
- **Environment configuration**: environment variables select the log storage backend and its parameters, and turn log exposure and learning mode on or off; a value coming from the environment beats the file and stays locked against the API.
- **Distribution**: a binary per platform and a Docker image.
- **External surface**: two ports — the proxy port (traffic) and the admin port (API + UI), so the panel never collides with the forwarded routes.
- **Build order**: the proxy core comes first and the interface last, so that every capability is verifiable through the API before any screen exists.
