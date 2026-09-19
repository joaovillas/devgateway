## 1. Project foundation

- [x] 1.1 Initialize the Go module and the `cmd/gateway`, `internal/...`, `web/` layout, and verify that `go build ./...` completes on a clean clone
- [x] 1.2 Commit a minimal `web/dist/index.html` and the `//go:embed all:web/dist` directive, and verify that `go build ./...` completes without the frontend having been built
- [x] 1.3 Create the `Makefile` (or `Taskfile`) with build, test and lint targets, and verify that each target runs from scratch
- [ ] 1.4 Set up CI running the build, `go vet` and the tests with `-race`, and verify that the pipeline passes on the first commit

## 2. Configuration

- [x] 2.1 Define the `gateway.json` structures (ports, seed, history storage, exposure and recording, capture limits, routes directory, schema version) with English keys, and verify with a serialization test that a sample file loads and reserializes without losing fields
- [x] 2.2 Define the route document structure (name, upstream, matching, overrides, schema version) with English keys, and verify with a test that a sample document loads and reserializes without loss
- [x] 2.3 Implement the scan of the routes directory and the merge into a snapshot, ignoring files without a recognized extension, and verify against the four scenarios of the "One document per route" requirement
- [x] 2.4 Implement collision detection between documents (duplicate name, identical host and pattern), and verify against the three scenarios of the corresponding requirement
- [x] 2.5 Implement validation with a message naming the file, the field and its location, including the refusal on a higher schema version, and verify against the four scenarios of the validation requirement
- [x] 2.6 Implement the environment-over-file-over-default precedence and the query for the effective origin of each value, and verify against the three scenarios of the corresponding requirement
- [x] 2.7 Implement the immutable snapshot behind an `atomic.Pointer` with precomputed indexes, and verify with a concurrency test under `-race` that reads concurrent with the swap never observe partial state
- [x] 2.8 Implement startup with separate ports and the refusal of identical ports, and verify against the two scenarios of the port separation requirement and the "Identical ports" scenario of the validation requirement
- [x] 2.9 Add to the route document the override's `enabled` field (on by default), the `source` block (learned or derived, originating exchange, incomplete body) and the swap of `preserveHost` for `rewriteHost`, and to `gateway.json` the `learning.enabled` key with its environment variable, and verify with a round-trip test and against the validation scenarios

## 3. Reverse routing

- [x] 3.1 Implement route resolution by ordered list with suffix wildcards and exact paths (host before path, more specific before less), and verify against the precedence scenarios of the wildcard routing and host routing requirements
- [x] 3.2 Implement forwarding on top of `httputil.ReverseProxy` with optional prefix stripping, and verify against the prefix stripping and preservation scenarios
- [x] 3.3 Implement the forwarding headers with `X-Forwarded-For` accumulation and optional `Host` preservation, and verify against the three scenarios of the header forwarding requirement
- [x] 3.4 Implement the upstream failure responses (`502` with no connection, `504` on timeout, `404` with no route) with a diagnostic body, and verify against the corresponding scenarios
- [x] 3.5 Verify traffic transparency with an end-to-end test exercising a preserved method and body, a passed-through status and a `text/event-stream` response arriving incrementally
- [x] 3.6 Make forwarding transparent — the original `Host` by default with an optional `rewriteHost`, `X-Forwarded-Host` and `X-Forwarded-Proto` preserved when already present, arbitrary and repeated headers passed through — and add the `X-Gateway` header on the way out and on the way back, and verify against the six scenarios of the header forwarding requirement and the scenarios of the gateway identification requirement (the "Identification with an intervention" scenario depends on overrides and is verified end to end in 6.10)

## 4. History storage

- [x] 4.1 Define the storage interface (record, list with filters, look up by identifier, navigate by cursor, clear) and the contract test battery that runs against any implementation, and verify that the battery fails against an empty implementation
- [x] 4.2 Implement the in-memory backend as a ring of configurable capacity, and verify against the contract battery and the "Capacity exceeded in memory" scenario
- [x] 4.3 Implement the NDJSON backend with append writes and indexed reads, and verify against the contract battery and the restart survival scenario
- [x] 4.4 Implement the SQLite backend with a pure Go driver, and verify against the contract battery, the restart survival scenario and a `go build` with `CGO_ENABLED=0` completing
- [x] 4.5 Implement backend selection by environment variable with memory as the default and a refusal to start when the backend fails, and verify against the "Memory is the default" and "An unavailable backend prevents startup" scenarios
- [x] 4.6 Implement the hot swap of the history backend (initialize the new one, swap, close the old one, no migration), and verify against the "History backend hot-swapped" and "An unavailable new backend preserves the current one" scenarios

## 5. Traffic capture

- [x] 5.1 Implement the exchange record with a unique identifier, request and response data, route, override and sizes, with body truncation at the configured limit, and verify against the three scenarios of the recording requirement
- [x] 5.2 Implement the timing broken down into total time, upstream time, injected time and gateway overhead, and verify against the three scenarios of the latency breakdown requirement
- [x] 5.3 Implement querying with reverse chronological order, pagination and combined filters, and verify against the four scenarios of the querying and filtering requirement
- [x] 5.4 Implement reads by identifier and item-by-item navigation honoring the active filters, and verify against the four scenarios of the single read and cursor navigation requirement
- [x] 5.5 Implement turning recording and exposure off independently, and clearing on demand, and verify against the three scenarios of the configurable exposure requirement

## 6. Overrides

- [x] 6.1 Implement the selection criteria (exact path, wildcard and regex; method, headers, query and body with the equality, regex, JSON equality and substring operators), and verify against the five scenarios of the selection criteria requirement
- [x] 6.2 Implement precedence by specificity between overrides, with ties broken by declaration order, and verify against the three scenarios of the precedence requirement
- [x] 6.3 Implement passthrough by default and the `501` for a route with no upstream and no matching override, and verify against the three scenarios of the selective interception requirement
- [x] 6.4 Implement the declared response with a default status of `200` and content type inference for a JSON body, and verify against the three scenarios of the declared response requirement, and verify again, with a response synthesized by a real override, the "A synthesized response records no upstream time" scenario of the `traffic-capture` spec (in 5.2 it was verified only through the capture record, with the intervention annotated by hand)
- [x] 6.5 Implement the source of randomness derived from `(seed, sequence number)` with `math/rand/v2`, and verify against the two scenarios of the determinism requirement, under `-race` and with concurrent requests
- [x] 6.6 Implement the application probability with passthrough when it is not drawn, and verify against the four scenarios of the corresponding requirement, including the statistical test of 1000 requests at 30%
- [x] 6.7 Implement fixed latency or latency drawn from a range, applied once the response is ready, including for an override with no `respond`, and verify against the five scenarios of the latency and drop requirement, and repeat with a real override (latency of `2s`, upstream of `150ms`) the "Injected time separated from real time" scenario of the `traffic-capture` spec, which in 5.2 was verified through the same injection point triggered by a test hook
- [x] 6.8 Implement the connection drop via `http.Hijacker` with the documented degradation on HTTP/2, and verify against the "A drop ends with no response" scenario and the drop scenario of the `traffic-capture` spec
- [x] 6.9 Implement expiry by time to live and by application count, with both queryable, and verify against the four scenarios of the expiry requirement
- [x] 6.10 Implement the intervention identification header and the marking on the captured exchange, and verify against the two scenarios of the identification requirement, against the "Identification with an intervention" scenario of the gateway identification requirement in the `gateway-routing` spec (the client receives `X-Gateway: route=payments; override=payments/flaky; intervention=synthesized` when the override synthesizes the response), against the three scenarios of the distinction requirement in the `traffic-capture` spec, and against the "Filter by intervention" scenario of that same spec with an exchange synthesized by a real override (in 5.3 the synthesized exchange was recorded through the capture path, without the override engine)
- [x] 6.11 Implement the override on/off switch, with disabled ones out of selection and precedence, and verify against the three scenarios of the enabled and disabled override requirement
- [x] 6.12 Implement learning mode — detecting unknown methods and paths after the upstream responds, generating the disabled override with the full response and its source, writing the document atomically and rebuilding the snapshot off the request path — and verify against the six scenarios of the endpoint learning requirement
- [x] 6.13 Implement segment parameters in rule paths (`/zip/:id/json`), with precedence between exact and regular expression, and the generalization in learning (identifier detection, knowledge by matching, absorption of the exact learned overrides it covers), and verify against the segment parameter path scenario and the three new learning scenarios
- [x] 6.14 Replace the override's single probability with a per-effect frequency (response, latency and drop, each with its own; absent means always; the override's own acts as the default for the ones that lack it), with an independent, deterministic draw per effect, and verify against the six scenarios of the per-effect frequency requirement

## 7. Admin API

- [x] 7.1 Implement the read and write endpoints for routes and overrides, and verify with API tests that each resource is created, changed, read and removed
- [x] 7.2 Implement per-document persistence with atomic writes and a write mutex, and verify against the four scenarios of the admin API requirement, including the byte-for-byte comparison of the untouched documents
- [x] 7.3 Implement reload on command, preserving the previous configuration on error, and verify against the reload, in-flight requests and invalid reload scenarios of the reload-without-restart requirement
- [x] 7.4 Implement the history endpoints — listing with filters, read by identifier, cursor navigation and clearing — and verify with API tests that each one honors the exposure configuration
- [x] 7.5 Implement the effective configuration endpoint with the origin of each value, and verify against the "Effective origin is queryable" scenario
- [x] 7.6 Implement deriving an override from a captured exchange, including the refusal for a nonexistent exchange and the handling of a truncated body, and verify against the three scenarios of the derivation requirement
- [x] 7.7 Implement the SSE stream of new exchanges batched to at most one update per second, and verify with a test that a connected client receives the exchanges in order and that the connection survives periods with no traffic
- [x] 7.8 Write the API reference and verify that every operation in the specs has an example runnable with `curl`
- [x] 7.9 Implement the read and write endpoints for the process configuration, writing to `gateway.json`, refusing values that come from the environment by naming the variable, and toggling learning mode, and verify against the "Process configuration changed through the API" and "A value from the environment is locked" scenarios
- [x] 7.10 Implement the listener supervisor with hot swapping of the traffic and admin ports, and verify against the "Port hot-swapped" and "An unavailable new port preserves the current one" scenarios

## 8. Web interface

- [x] 8.1 Set up the Vite project with React and TypeScript, the API client and the build to `web/dist`, and verify that the built binary serves the interface on the admin port with no access to the external network, per the two scenarios of the requirement that the binary serves the interface itself
- [x] 8.2 Implement the services panel in two views, the map (your app → services → destinations, connected; the default view) and the list (entry → destination), with the choice remembered across sessions, search and "+ service" always visible in both, scaling to 50+ services, highlighting for an active rule, flagging for an unavailable destination and filtering by service or destination selection, and verify against the eight scenarios of the services in map and list requirement
- [x] 8.3 Implement the override's continuous controls applied immediately, with the remaining time and applications and turning it off in a single gesture, and verify against the three scenarios of the direct control requirement
- [x] 8.4 Implement the traffic list and detail with a waterfall separating upstream time from injected time, intervention flagging, item-by-item navigation and display of truncated bodies, and verify against the four scenarios of the inspection with waterfall requirement
- [x] 8.5 Implement the indication of a disabled history as distinct from an empty one, and verify against the two scenarios of the corresponding requirement
- [x] 8.6 Implement creating an override from a displayed exchange, with review before it takes effect, and verify against the two scenarios of the corresponding requirement
- [x] 8.7 Implement consuming the SSE stream with disconnection flagging and automatic reconnection, and verify against the two scenarios of the real-time update requirement
- [x] 8.8 Implement the comment loss warning before the first write to a route document that contains comments, and verify that the warning appears once and does not reappear after confirmation
- [ ] 8.9 Implement editing with parity through controls only — every route, override and process field editable in the interface, without showing the documents' YAML or JSON, with validation errors next to the control and values from the environment locked — and verify against the four scenarios of the editing with parity requirement
- [x] 8.10 Implement the override and learning mode on/off switches, the distinction of learned overrides and access to the originating exchange, and verify against the three scenarios of the corresponding requirement
- [x] 8.11 Adopt the user's vocabulary throughout the interface (your app, service, entry, destination and rule instead of client, route, matching, upstream and override), keeping the internal terms only in identifiers, the API and the files, and verify against the scenario of the user vocabulary requirement
- [x] 8.12 Display and edit paths with segment parameters in the interface (the `:id` highlighted as a parameter, accepted when creating and editing a rule), and verify by creating a `/zip/:id/json` rule through the interface
- [x] 8.13 Implement simple mode (the default) and advanced mode, with the choice remembered and the summary flagging advanced features, and verify against the four scenarios of the corresponding requirement
- [ ] 8.15 Display the per-effect frequency in the panel ("responds 503 in 30% of the calls"), with no global probability, and mark as inactive a rule whose effects are all at 0%, and verify against the corresponding scenarios
- [x] 8.14 Trim the traffic inspection (essential columns, the intervention next to the status, the service column only when unfiltered, filters collapsed with the active ones visible), and verify against the three scenarios of the trimmed traffic requirement

## 9. Distribution and documentation

- [x] 9.1 Produce per-platform binaries and the Docker image in the same build that embeds the frontend, and verify that the image comes up and serves on both ports
- [x] 9.2 Write the README with installation, a sample `gateway.json` and route document, the environment variables, the criterion behind the two formats, and the assumed limits (comment loss on writes and on learning, determinism by arrival order, connection drops on HTTP/2, learning paths with identifiers, backend swaps without migration), and verify that a reader can bring the gateway up following the document alone
- [x] 9.3 Set up an example environment with two toy services and ready-made route documents, and verify end to end that passthrough, a forced override, a probabilistic override, latency from a range, the waterfall, learning mode and a hot port swap all work on it
