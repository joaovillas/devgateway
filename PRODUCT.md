# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Stack

Vite + React + TypeScript frontend in `web/`, built to `web/dist` and embedded in the Go binary via `go:embed` (decided in `openspec/changes/add-test-gateway/design.md`). No network dependency at runtime: fonts and static assets ship inside the binary.

## Users

A single developer, on their own machine. The panel sits on a second screen or tab next to the editor and the client they are building. They switch between writing code, firing requests and looking at what the gateway did with them. There is no dedicated operator, and the panel is a support tool, consulted in short glances during the work.

## Product Purpose

A self-hosted HTTP gateway for development, shipped as a single Go binary. It puts N services behind one port, and with that it can provoke on demand the conditions that break the system in production: a forced response, an intermittent error, latency and a dropped connection. That way retry, timeout and circuit breaker code gets exercised before it goes to production. Success is when the developer can provoke and observe a failure without leaving their workflow.

## Positioning

A single concept, the override, replaces the split between mock and chaos. Forcing a response is injecting chaos with probability `1.0`. MockServer spreads that same engine across 21 screens on top of the JVM, and Smocker has no percentage error rate. Neither shows the `client → route → upstream` topology: both hand you nothing but a log list. The waterfall separates the upstream's real time from the time the gateway injected.

## Operating Context

- Two ports: traffic (`8080` by default) and administration (`8081` by default). The panel lives only on the admin one.
- The source of truth is the files versioned in the team's repository: `gateway.json` for the process and one YAML document per route in `routes/`.
- The admin API covers every operation, and the panel is a client of that same API. Everything the panel does can also be done with `curl`.
- New exchanges arrive over SSE, batched to at most one update per second.

## Capabilities and Constraints

- **Parity with the files (a user requirement).** The panel never shows the YAML or the JSON: it works through visual controls only. Everything configurable by editing `gateway.json` or the route YAML MUST also be configurable from the panel, and vice versa. The two paths are equivalent.
- Routes: name, upstream, matching by host and/or path (exact or suffix wildcard), prefix stripping, Host preservation and timeout.
- Overrides: selection by path (exact, wildcard or regex), method, headers, query and body (the equals, regex, json and contains operators). They also declare a response (status, headers and body), a probability, latency (fixed or from a range), a connection drop, a TTL and an application limit.
- Precedence by specificity between routes and between overrides. A deterministic seed by arrival order.
- History with a memory, NDJSON or SQLite backend. Exposure and recording can be turned off independently. There is querying with filters, reads by id and item-by-item navigation.
- Configuration precedence: environment > `gateway.json` > default. The origin of each value is queryable.
- Assumed limits: writing through the API rewrites the route document and loses comments and key order. On HTTP/2, a connection drop becomes a stream cancellation.
- Everything changes at runtime, ports and history backend included. Values that come from the environment appear locked in the panel, with the variable named.

## Brand Commitments

No product name yet. The repository calls the product "gateway", which is the provisional name. There is no logo, voice or defined identity. Do not invent a brand.

## Evidence on Hand

There are no users, testimonials, metrics or real screenshots. The configuration examples live in `internal/config/testdata/`. The demo routes and traffic will come from the example environment (task 9.3). No made-up performance numbers, customers or comparisons.

## Product Principles

1. **One concept, one gesture.** Mock and chaos are the same override. The interface never splits them into different places.
2. **File and screen are equivalent, without mixing.** What you do in one you can do in the other; the file is for the editor, the screen is controls only, and the panel never displays the document.
3. **Show the path, not just the log.** The topology and the time breakdown explain what happened. A list on its own is not enough.
4. **Do not pull the developer out of the flow.** Reading at a glance, an immediate adjustment with no save step, and nothing that requires syncing or reloading.
5. **Never hide why something is the way it is.** The origin of each value, the override responsible, a disabled history and a disconnection are always explicit.

## Accessibility & Inclusion

No product-specific requirement has been set beyond the general quality floor: keyboard navigation, contrast and continuous controls operable without a mouse.
