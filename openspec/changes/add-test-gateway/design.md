## Context

Greenfield project: the repository holds only the OpenSpec scaffold. The environment already has Go 1.26, Node 24 and Docker. See `proposal.md` — Why for the motivation, and this change's `specs/` for the requirements.

Three constraints shape every decision below:

- **A single artifact.** The product has to be a static binary with no runtime dependency. That decides the database driver and pushes the frontend inside the executable.
- **A development environment, not production.** The volume is tens of requests per second, not tens of thousands. Clarity and predictability are worth more than throughput.
- **The interface comes last.** Every capability has to be exercisable through the admin API before any screen exists, or the project ends up hostage to the frontend.

## Goals / Non-Goals

**Goals:**

- One single concept for intervention, instead of mock and chaos as parallel mechanisms.
- A request path with explicit steps in a fixed order, so the capture waterfall is a direct reading of what the gateway did and not an approximate reconstruction.
- Real determinism under concurrency when a seed is declared.
- Configuration swaps with no lock on the hot path and without dropping in-flight requests.
- Configuration that does not produce merge conflicts when two people create different routes.
- An admin API complete enough to drive the entire gateway from `curl`, with the interface as a client of that same API.

**Non-Goals:**

- Optimizing throughput or allocation. Correctness and readability come first.
- Terminating TLS on the traffic port in this change (see Open Questions).
- Authentication on the admin port in this change (see Open Questions).

## Decisions

### One override instead of mock and chaos

There are not two mechanisms. There is the override: a selector plus the effects `respond`, `latency` and `drop`, each with its own frequency, plus expiry by `ttl` and by count. Always responding is a forced response; responding at `0.3` is fault injection; whatever is not drawn goes on to the upstream.

*Why:* the reference tools separate mock from chaos, and the separation leaks to the user — in MockServer you configure an expectation on one screen and a chaos profile on another to control the same route. The distinction is internal, not conceptual: forcing a response is injecting chaos at maximum probability. Unifying them cuts half the configuration surface, half the UI, and the question "do I set this up as a mock or as chaos?".

*A consequence worth recording:* an override with `latency` and no `respond` intercepts nothing — it just delays the upstream's response. That falls out of the model naturally and covers the "I only want this endpoint to be slow" case without inventing a third concept.

*Alternative rejected:* keeping both under distinct names, mirroring MockServer and Smocker. Rejected because it preserves an overlap the user spotted before the code existed.

### Proxying on `net/http/httputil.ReverseProxy`

Use the standard library's `ReverseProxy` with the `Rewrite` API, instead of writing the forwarding from scratch or adopting `fasthttp`.

*Why:* streaming, incremental `Flush`, trailers, HTTP/2 and `Expect: 100-continue` already come solved and tested. Writing that by hand is where homemade proxies go wrong. `fasthttp` would be faster, but it does not speak HTTP/2, its API diverges from the stdlib, and speed is not the bottleneck here.

### One fixed request path, in a fixed order

Every request on the traffic port goes through exactly this sequence:

1. Resolve the route (host, then the most specific path pattern).
2. Open the capture record and start the clock.
3. Resolve the applicable override: among the enabled ones that select the request, the most specific.
4. If there is an override, draw each declared effect in a fixed order — drop, response, delay — from a single source of randomness, each against its own frequency.
5. An effect that was not drawn is ignored; with none drawn, go to the upstream as if the override did not exist.
6. If there was a drop: close the connection and close the record.
7. If the override declares `respond`: synthesize the response. Otherwise, forward to the upstream.
8. Apply the drawn delay **after** the response is ready and before writing it to the client — whether it is the upstream's, the synthesized one, or an error response from the gateway itself (`501`, `502`, `504`). If the client gives up during the delay, nothing was delivered to it: the exchange ends with no status, with the abandonment (`client_canceled`) recorded.
9. Close the record with the times broken down and, with learning mode on, learn the endpoint off the request path.

*Why the delay at step 8 and not before the upstream:* delaying afterwards keeps the upstream's real time and the injected time as independent, directly measurable quantities. Delaying beforehand would force subtracting one from the other to show the waterfall, and the subtraction is wrong whenever the upstream fluctuates. The cost is that total latency becomes the sum rather than the maximum — which is exactly the behavior expected by someone who asks for "this route takes 2s longer".

*Why one frequency per effect:* "this endpoint fails in 30% of the calls and is always slow" is a single sentence, and with one probability for the whole override it would need two overrides on the same path. Drawing effect by effect, each one answers the question "in how many calls does this happen?".

*Why draw everything at step 4:* the draw has to be independent of what happens afterwards. If application were decided only once the upstream responds, the same seed would produce different sequences depending on upstream availability, and determinism would be gone.

### Determinism by sequence number, not by content

Every request gets a monotonic sequence number on the way in. When a seed is configured, that request's source of randomness is derived from `(seed, sequence)`, using `math/rand/v2` and a PCG generator. One request's decisions do not depend on any other's.

*Why:* a single generator shared across goroutines would produce results that depend on scheduling order — apparent determinism, which breaks under load. Deriving by sequence gives genuine reproducibility with zero contention on the draw.

*Alternative rejected:* deriving from a hash of the request. That would be deterministic by content, but then the same repeated request would always or never be intercepted — which destroys the whole point of "applies 30% of the time".

*Limit assumed and documented:* determinism is about **arrival order**. Reproducing a run requires replaying the requests in the same order.

### Configuration in one document per route

`gateway.json` holds the process configuration. Each route lives in its own YAML document under `routes/`, and the load merges them all into a snapshot.

*Why:* it is the pattern of `nginx conf.d` and of Kubernetes manifests, and it solves three problems at once. Two people creating different routes never touch the same file, so there are no merge conflicts. The PR diff now says "added the payments route" instead of showing a smudge in the middle of a big file. And writing through the API becomes surgical: touching one route rewrites only that document.

*Explicit trade-off:* reserialization **does not preserve comments or the original key order** of the document it touches. Preserving them would mean manipulating the YAML syntax tree and keeping that manipulation correct through every schema change — a disproportionate cost. The decision is to accept the loss, which is now confined to one route document instead of the whole file, to document it in the README and to warn in the interface before the first destructive write.

### JSON for the process, YAML for the routes

The two formats coexist on purpose.

*Why:* `gateway.json` is read by machines and by bootstrap scripts, almost never edited by hand, and JSON avoids type ambiguity. Route documents are hand-edited all the time, and YAML survives that better — comments, multi-line body blocks and less punctuation noise. Forcing a single format would optimize documentation consistency at the expense of the people using it.

### Precedence: environment over file over default

Environment variables beat `gateway.json`, which beats the built-in defaults. The effective origin of each value can be queried through the API.

*Why:* the storage backend changes per environment — memory on the developer's machine, SQLite in CI — while the routes stay the same. The environment is the right place for what varies per machine; the file, for what varies per project. The origin query exists because the question "why is it using memory if I configured SQLite?" needs an answer in one command, not in a debugging session.

### Resolution by ordered list, on two levels

Routes and overrides are sorted by specificity at load time and resolved by a linear scan — the route first, then the override inside it.

*Why:* a prefix tree would be faster and much harder to audit when the user asks why a given request landed on a given override. With the dozen or so routes typical of a development environment, the scan is irrelevant in the profile, and the precedence order stays readable in the code itself.

### Configuration as an immutable snapshot swapped atomically

The merged snapshot lives in an `atomic.Pointer` to an immutable struct, with the routing indexes precomputed. A reload validates, builds a new snapshot and swaps the pointer. In-flight requests carry on with the snapshot they captured on the way in.

*Why:* it removes locks from the hot path and solves for free the requirement that a reload must not disturb in-flight requests. Validating before swapping is what guarantees an invalid configuration preserves the previous one — the swap happens only once the new snapshot exists in full.

### History storage behind one interface, with three implementations

The history is reached through a single interface, with implementations in memory (a ring, the default), an NDJSON file and a local SQLite database, chosen by environment variable. The same contract test battery runs against all three.

*Why:* an in-memory history is enough on the developer's machine, but not to investigate what happened in a CI run that already finished, nor to correlate two runs. NDJSON serves anyone who wants to process it with `jq` without standing up a database. SQLite serves filtered queries over large volumes, which is exactly where a flat file degrades.

*The constraint that decides the driver:* the common SQLite driver in Go uses CGO, which would break cross-compilation and the static binary. The implementation MUST use a pure Go driver (`modernc.org/sqlite`). It is slower, and in this use that does not matter.

*History ordered by arrival, not by completion:* an exchange is written only when it finishes, but the history places it by arrival — start instant, then sequence number, with write order only as a tiebreaker. That way a slow request that arrived earlier does not show up as newer than the fast ones that arrived after it, and the listing, the pagination and the item-by-item navigation agree with the instants on display. The pagination cursor carries that key and the history epoch, which advances on every clear.

*Why a failed initialization does not fall back to memory:* silently coming up on a different backend would produce the worst kind of error — everything working, nothing being persisted, found out hours later. Better to refuse to start.

### Real time over SSE, not WebSocket

The interface receives new exchanges over *Server-Sent Events*, batched to at most one update per second.

*Why:* the flow is one-way — the interface only consumes, and every write already goes through the REST API. `EventSource` reconnects on its own, which meets the automatic reconnection requirement without any retry code of ours. WebSocket would bring bidirectionality that would go unused and a reconnection path to maintain by hand.

### Connection drops limited to HTTP/1.1

The drop uses `http.Hijacker` to close the socket without writing a response. That does not exist in HTTP/2.

*Decision:* on HTTP/2 the drop degrades to an abrupt stream cancellation, and the capture records which of the behaviors happened in `dropMode`: `hijack` (HTTP/1.1, socket closed) or `stream_reset` (HTTP/2). A third value, `abort`, covers the rare case of an HTTP/1.x connection whose `ResponseWriter` cannot be hijacked (an intermediate writer with no `Unwrap`, for instance): the handler is aborted with `http.ErrAbortHandler` and the server closes the connection with no response. Recording it as `hijack` or `stream_reset` would hide from the developer what actually happened. The alternative — refusing to configure a drop when the port serves HTTP/2 — was rejected because it makes the behavior depend on a transport detail the user did not choose.

### Frontend embedded via `go:embed`

The frontend is React + TypeScript built with Vite into `web/dist`, embedded with `//go:embed all:web/dist`. A minimal `index.html` is committed in that directory so `go build` works on a clean clone without requiring Node. During frontend development, the Vite server proxies the API calls to the admin port.

*Why the committed placeholder:* without it, `go build` breaks on any machine that has not run the frontend build first — including CI and the machine of anyone who only wants to touch the backend.

### Two ports, no reserved paths, hot-swappable

Traffic and administration sit on separate ports (`8080` and `8081` by default), and the process refuses to start if they are the same.

*Why:* any reserved path on the traffic port would be a prefix the user could not use in their own routes. It is the same separation Smocker adopts, and for the same reason.

*Hot swap:* the listeners live in a supervisor, outside the snapshot. Changing a port opens the new listener first; only if it opens does the old server get `Shutdown`, which stops accepting connections and lets in-flight requests finish. If the new listener fails, nothing changes and the error is reported. Swapping the history backend follows the same protocol: initialize the new one, swap the pointer, close the old one. The history is not migrated between backends — migrating would mean rewriting arbitrary volumes inside a configuration request, and the question "where are my old exchanges?" has a simple answer: in the previous backend, intact.

### Transparency: pass everything through, add one header

The gateway passes through method, path, query, body, every header and the `Host` as they arrived, and only adds the `X-Forwarded-*` headers (preserving values already present) plus a single header of its own, `X-Gateway`, on the way out and on the way back. Hop-by-hop headers are the only removal, because HTTP forbids forwarding them; WebSocket `Upgrade` and trailers keep working through `ReverseProxy`.

*Why:* the gateway exists to test the client against the real service; any silent change invalidates the test. Keeping the original `Host` by default follows the same logic; a route declares `rewriteHost` when the upstream requires its own host, as with virtual-host servers.

*One header only:* `X-Gateway` carries the route and, when there is an intervention, the override and its kind (`route=payments; override=payments/flaky; intervention=synthesized`). When the override both synthesizes and delays, both interventions appear, comma-separated and in the order they happen (`intervention=synthesized,delayed`), just like the `interventions` list of the captured exchange. Several `X-Gateway-*` headers would be easier to read one by one, but would multiply what the gateway injects into the traffic.

### Learning writes disabled overrides

With learning mode on, every new method and path answered by the upstream becomes an `enabled: false` override in the route document, with the real response pre-filled and a `source` block recording the originating exchange.

*Why:* the learned endpoint is already the starting point for the gesture the user wants to make next — turning on chaos or customizing the response. Keeping it as a parallel list of "known endpoints" would create a second concept alongside the override, exactly the separation this project refuses. Disabled by default, it does not change the traffic until the user decides to.

*Generalization:* segments that look like an identifier (all digits, a UUID, hexadecimal, or alphanumeric with digits and at least 8 characters) become the segment parameter `:id`, and the rest of the path stays literal. The heuristic is deliberately conservative: `me`, `json`, `charge` never become a parameter, and a false negative only costs one extra rule, which the user generalizes by hand. The segment parameter sits in the precedence between the exact path and the regular expression, with ties broken by the number of literal segments.

*How:* learning runs after the response has been delivered, off the request path, and writes through the same mechanism as the API — an atomic write of the document under the write mutex, followed by rebuilding the snapshot. A combination is known when the route already has an override of the same method with the same generalized path, or with an exact or parameterized path that matches the request; free wildcards and regular expressions do not count, so that endpoints underneath them get learned too. The absorption of exact learned overrides by the generalized one reaches only those still in the state learning left them — disabled and with no other criteria; one the user enabled or restricted stays, taking precedence over the generalized one.

*Fidelity of the learned response:* a repeated header (several `Set-Cookie` headers, for instance) is written as a list of values in `respond.headers` — each header accepts a string or a list — and the synthesized response repeats it, one per value. A JSON body only becomes a structure when every number fits without loss (integers up to `uint64`, decimals `float64` reproduces); otherwise it stays as text, identical to what was observed. A path containing a literal `*` (or one that does not start with `/`) cannot be written as an exact path, because the `*` would be a wildcard: the criterion written is an anchored `pathRegex` (`^` + escaped path + `$`), which also counts as an exact path when checking for a known endpoint. An assembled override that still fails validation is not written, and the endpoint stops being a candidate until the process restarts, with a single warning in the log.

*Originating exchange without a history:* with `history.record` off, the exchange is observed by learning but does not reach the history; the learned override then comes out without `source.exchange`, so it does not point at a nonexistent exchange, and keeps `source.kind`, `source.at` and `source.bodyIncomplete`. A derived override (which always starts from an exchange in the history) still requires `source.exchange`.

## Risks / Trade-offs

- **Injected latency adds instead of overlapping** → Accepted knowingly to keep the waterfall exact. Document in the interface's latency field that the value is added to the upstream's real time.
- **Upstream time includes waiting on a slow client during streaming** → The response body is copied to the client without being buffered; when the client reads more slowly than the upstream sends, the copy waits on it and that wait lands in the upstream time. Separating them would require buffering the entire response, which would break streaming. Accepted and documented in the exchange model (`exchange.Timing`).
- **Determinism depends on arrival order** → Document it next to the seed configuration. Concurrent requests may get sequence numbers in a different order from run to run; strict reproducibility requires serial sending.
- **Connection drops behave differently on HTTP/2** → Record in the capture which behavior happened, so the developer never has to guess.
- **Route document comments are lost when writing through the API** → Atomic per-document writes, a warning in the README and an alert in the interface before the first destructive write. The damage is confined to one route.
- **Identifiers the heuristic does not recognize** (slugs such as `/posts/my-title`) still produce one rule per value → the user replaces it with `:id` in the rule, and the other learned rules it covers stop being generated.
- **Learning rewrites the route document** → It inherits the comment loss from API writes; the interface and README warning applies here too.
- **Swapping the backend does not migrate the history** → Recorded in the API response and in the interface at the moment of the swap.
- **Three storage backends triple the test surface** → A single contract test battery runs against all three implementations; none gets its own tests except for what is specific to it.
- **A pure Go SQLite driver is slower than the CGO-based one** → Irrelevant at the target volume, and it is the price of keeping the binary static and cross-compilable.
- **Two configuration formats can confuse** → The criterion is simple and goes in the README: the process in JSON, the routes in YAML. One format per kind of reader.
- **The interface comes last and may run out of steam** → Mitigated by the decision that the API covers every operation: even with no screen at all, the product is usable through `curl` and through route documents.
- **`ReverseProxy` rewrites headers on its own** → Cover the header scenarios of the routing spec with tests, especially the accumulation of `X-Forwarded-For` and the optional preservation of `Host`.

## Migration Plan

There is no migration: the project has no prior code and no consumers.

For distribution, both `gateway.json` and every route document carry a schema version field from the first version onward. The gateway refuses to load a document whose schema version is higher than the one it knows, with an explicit message — this keeps a future configuration from being half-interpreted by an old binary.

Delivery: per-platform binaries and a Docker image, both produced by the same build that embeds the frontend.

## Open Questions

- **TLS termination on the traffic port.** Today the gateway speaks plain HTTP to the client and may speak HTTPS to the upstream. Accepting HTTPS from the client would require certificate management and probably a local authority. It changes none of this change's specs and can be added later as port configuration.
- **Authentication on the admin port.** The target use is local. If the gateway ends up shared by a team on an internal network, we will have to choose between a static token and an authenticating proxy in front. Neither option changes the API or the current specs.
- **Retention on the persistent backends.** In memory the ring handles it. On NDJSON and SQLite, the history grows without bound until someone deletes it. Decide later between rotation by size, by age, or none — it does not change the read contract already specified.
