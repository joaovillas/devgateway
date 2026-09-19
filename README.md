# devgateway

[![CI](https://github.com/joaovillas/devgateway/actions/workflows/ci.yml/badge.svg)](https://github.com/joaovillas/devgateway/actions/workflows/ci.yml)

A configurable development gateway: put your services behind one port and inject failures on demand.

devgateway sits in front of the services you run locally, routes traffic to each one by path or by host, and lets you **interfere with that traffic on purpose**: answer in place of a service, fail a fraction of the calls, add latency, drop the connection. Everything that goes through it is recorded, with the time split between what the service took and what the gateway injected.

It is a single Go binary, with no runtime dependencies and the web panel built in.

- **Traffic port** (`8080` by default): where your client talks. The gateway is transparent — method, path, query, body, headers and `Host` go through as they arrived. It only adds the `X-Forwarded-*` headers and one of its own, `X-Gateway`.
- **Admin port** (`8081` by default): the web panel and the REST API the panel uses. Everything the panel does has a `curl` equivalent in [`docs/api.md`](docs/api.md).

Contents: [why](#why-devgateway-exists) · [install](#install) · [quick start](#quick-start) · [`gateway.json`](#gatewayjson) · [route documents](#route-documents) · [environment variables](#environment-variables) · [the override](#the-override-mocking-and-chaos-are-the-same-thing) · [the panel](#the-panel) · [learning mode](#learning-mode) · [hot reconfiguration](#hot-reconfiguration) · [the API](#the-api) · [example environment](#example-environment) · [development](#development) · [assumed limits](#assumed-limits)

## Why devgateway exists

Mock servers such as MockServer and Smocker let you stand in for a service; chaos tools let you break one. Doing both usually means running two things, learning two vocabularies, and keeping two sets of rules in sync with the same endpoint.

devgateway has a single concept, the **override**: a selection criterion, an optional declared response, and the modifiers `latency`, `drop`, `ttl` and `maxApplications`. Each effect carries its own frequency, so "this endpoint fails in 30% of the calls and is always slow" is one rule. A mock is an override that always applies. Chaos is the same override with a frequency on the failure. Slowness is the same override with no declared response. One rule, one document, one place to look.

Two more consequences fall out of that:

- **A route forwards by default.** You do not have to describe a whole service to intercept one endpoint of it. Everything you did not override still reaches the real upstream.
- **Everything is a file.** Routes are YAML documents on disk; the process is a `gateway.json`. The API and the panel edit those same files, so what you tried by hand is what you commit.

## Install

### Binary

Needs Go 1.26 and, for the panel, Node 24.

From the root of a clone of the repository:

```sh
make release
```

`make release` builds the panel (`web/dist`) and produces one static binary (`CGO_ENABLED=0`) per platform in `dist/`:

```text
dist/devgateway_<version>_linux_amd64
dist/devgateway_<version>_linux_arm64
dist/devgateway_<version>_darwin_amd64
dist/devgateway_<version>_darwin_arm64
dist/devgateway_<version>_windows_amd64.exe
```

Copy the one for your platform into a directory on your `PATH` as `devgateway` (`devgateway.exe` on Windows). There is nothing else to install.

Without `make` (on Windows, for instance), the same steps by hand, for the current platform only:

```sh
cd web && npm ci && npm run build && cd ..
CGO_ENABLED=0 go build -trimpath -o devgateway ./cmd/devgateway     # devgateway.exe on Windows
```

Without Node, `go build ./cmd/devgateway` works too: the binary ships a placeholder page instead of the panel, and the API and the proxy work as usual.

### `go install`

```sh
go install github.com/joaovillas/devgateway/cmd/devgateway@latest
```

This builds from the module cache, which has no `web/dist` build in it, so the binary serves the placeholder page instead of the panel. The proxy, the API and learning mode are complete. For the panel, build from a clone as above or use the Docker image.

### Docker

From the root of a clone of the repository:

```sh
docker build -t devgateway .
docker run --rm -p 8080:8080 -p 8081:8081 devgateway
```

That starts with no routes; create them in the panel at <http://localhost:8081> or through the API. To use your own files, mount the directory holding `gateway.json` and `routes/` at `/data`:

```sh
docker run --rm -p 8080:8080 -p 8081:8081 -v "$PWD:/data" devgateway
```

In Git Bash on Windows, which rewrites paths passed to native programs, use `MSYS_NO_PATHCONV=1 docker run ... -v "$(pwd -W):/data" devgateway`; in PowerShell, `-v "${PWD}:/data"`.

The `Dockerfile` builds the panel with Node and the binary with Go, and ships a minimal final image (distroless, no shell) holding only the binary, running as an unprivileged user. In the image:

- the configuration is read from `/data/gateway.json` (`GATEWAY_CONFIG`), and the routes from `/data/routes`;
- `/data` is a volume. The API and learning mode write to those files, so the directory has to be writable by the container user. On Linux, add `--user "$(id -u):$(id -g)"` to write as yourself;
- with no `gateway.json`, the gateway starts with the defaults and no routes, and creates the file on the first change made through the API or the panel;
- to reach services running on the host machine, use `http://host.docker.internal:<port>` as the `upstream` (on Linux, with `--add-host=host.docker.internal:host-gateway`).

`make docker` runs `docker build` stamping the code's version into the image.

## Quick start

In an empty directory, create `gateway.json`:

```json
{
  "schemaVersion": 1,
  "ports": { "traffic": 8080, "admin": 8081 },
  "seed": 42
}
```

and `routes/payments.yaml`, pointing at a service of yours (here, one listening on `localhost:9001`):

```yaml
schemaVersion: 1
name: payments
upstream: http://localhost:9001
match:
  path: /payments/*
stripPrefix: true
overrides:
  - name: flaky
    match:
      path: /payments/balance
      method: GET
    respond:
      status: 503
      chance: 0.3
      body:
        error: unavailable
```

Start the gateway in that directory:

```sh
devgateway                     # or: devgateway -config path/to/gateway.json
```

and talk to it:

```sh
curl -i localhost:8080/payments/balance      # a synthesized 503, 30% of the time
curl -s localhost:8081/api/status            # process state
curl -s localhost:8081/api/exchanges         # what went through the gateway
```

The panel is at <http://localhost:8081>.

An intercepted response carries `X-Gateway: route=payments; override=payments/flaky; intervention=synthesized`; a forwarded one, just `X-Gateway: route=payments`. A path no route matches gets a `404` with `{"error": "no_route"}`.

## Two formats: JSON for the process, YAML for the routes

One format per kind of reader:

- **`gateway.json`** configures the process (ports, seed, history, learning). It is read by machines and by bootstrap scripts, almost never edited by hand, and JSON has no type ambiguity.
- **`routes/*.yaml`** describes the routes, one document per route. These are edited by hand all the time, and YAML takes comments and multi-line bodies and has less punctuation. One file per route also keeps two people creating different routes out of each other's merge conflicts.

Both declare a `schemaVersion`. The gateway refuses a document whose version is newer than the one it knows, instead of reading half of it.

## `gateway.json`

Every field is optional: what is missing comes from the environment variable, or from the default. The file itself is optional too; without it the gateway starts on the defaults and says so in the log.

```json
{
  "schemaVersion": 1,
  "ports": {
    "traffic": 8080,
    "admin": 8081
  },
  "seed": 42,
  "history": {
    "backend": "sqlite",
    "path": "data/history.db",
    "capacity": 1000,
    "record": true,
    "expose": true
  },
  "capture": {
    "maxBodyBytes": 65536
  },
  "learning": {
    "enabled": false
  },
  "routesDir": "routes"
}
```

| Key | Default | Meaning |
|---|---|---|
| `ports.traffic` | `8080` | port for forwarded traffic |
| `ports.admin` | `8081` | port for the panel and the API; must differ from the traffic port |
| `seed` | absent | seed for the draws; absent, randomness does not repeat across runs |
| `history.backend` | `memory` | `memory` (in-memory ring), `ndjson` (one file, one exchange per line) or `sqlite` (local file, pure-Go driver) |
| `history.path` | `gateway-history.ndjson` or `gateway-history.db` | file used by `ndjson` and `sqlite` |
| `history.capacity` | `1000` | exchanges kept by the `memory` backend |
| `history.record` | `true` | record the exchanges |
| `history.expose` | `true` | allow reading the history through the API and the panel |
| `capture.maxBodyBytes` | `65536` | larger bodies are truncated on capture (traffic is unaffected) |
| `learning.enabled` | `false` | [learning mode](#learning-mode) |
| `routesDir` | `routes` | directory holding the route documents |

Relative paths (`history.path`, `routesDir`) resolve against the directory of `gateway.json` itself, not against the process's working directory. If the history backend fails to initialize (a file with no write permission, say), the gateway refuses to start instead of silently falling back to memory.

## Route documents

Every `.yaml` or `.yml` file in `routes/` is one route; other files are ignored. Two documents with the same `name`, or with the same host and path, stop the load, and the message names both files. A validation error names file, field, line and column.

```yaml
# routes/payments.yaml
schemaVersion: 1
name: payments
upstream: http://localhost:9001
match:
  path: /payments/*          # suffix wildcard; without *, an exact path
  # host: payments.local     # instead of, or on top of, the path
stripPrefix: true            # /payments/balance reaches the upstream as /balance
rewriteHost: false           # true: the upstream gets its own host instead of the original Host
timeout: 5s                  # no response within this time and the client gets a 504

overrides:
  # Forced response: with no frequency, it applies whenever the criteria match.
  - name: charge-declined
    match:
      path: /payments/charges
      method: POST
      headers:
        X-Scenario: declined
    respond:
      status: 402
      headers:
        Content-Type: application/json
      body:
        error: card_declined

  # Fails 30% of the lookups and is slow in all of them: each effect has its
  # own frequency, drawn independently. The 70% that are not failed still go
  # to the upstream — late.
  - name: balance-flaky
    match:
      path: /payments/balance
      method: GET
    respond:
      status: 503
      chance: 0.3
      headers:
        Retry-After: "1"
      body:
        error: balance_unavailable
    latency: 800ms

  # Latency only: with no respond, the response comes from the upstream, late.
  - name: lookup-slow
    match:
      pathRegex: ^/payments/charges/[^/]+$
      method: GET
    latency:
      min: 200ms
      max: 900ms

  # Drops the connection with no response on 10% of the calls, for 10 minutes
  # or 50 applications, whichever comes first.
  - name: flaky-network
    match:
      path: /payments/*
    drop:
      chance: 0.1
    ttl: 10m
    maxApplications: 50

  # Off: it stays in the document, but never intervenes.
  - name: maintenance
    enabled: false
    match:
      path: /payments/*
    respond:
      status: 503
```

Route:

| Field | Meaning |
|---|---|
| `name` | unique route name |
| `upstream` | the service's URL. Without it, only the overrides answer, and everything else gets a `501` |
| `match.path` | exact path (`/health`) or suffix wildcard (`/api/payments/*`); segment parameters (`:id`) only in an override's path |
| `match.host` | matches on the request's `Host`; routes with a host take precedence |
| `stripPrefix` | strips the fixed part of the pattern before forwarding |
| `rewriteHost` | replaces the original `Host` with the upstream's (for virtual-host servers) |
| `timeout` | how long to wait for the upstream (`504` when it runs out); with no connection, `502` |
| `overrides` | list of overrides |

Among routes, the most specific one wins: host before path, exact path before wildcard, longer wildcard before shorter.

Override:

| Field | Meaning |
|---|---|
| `name` | unique within the route; the override is identified as `route/name` |
| `enabled` | `false` turns it off; absent means on |
| `match.path` / `match.pathRegex` | exact path (`/zip/01001000/json`), path with segment parameters (`/zip/:id/json`), suffix wildcard (`/zip/*`) or regular expression (one of the two fields). Each `:name` matches exactly one non-empty segment: `/zip/:id/json` matches `/zip/40415345/json`, not `/zip/40415345/extra/json`. The name follows `[A-Za-z_][A-Za-z0-9_]*`, does not repeat within a path, and does not share a segment with the wildcard. The path is the one the client sent, before `stripPrefix` |
| `match.method` | HTTP method |
| `match.headers`, `match.query` | by name, either a string (equality) or an object with one of `equals`, `regex`, `json`, `contains` |
| `match.body` | the same, applied to the body (`json` compares structure, ignoring formatting and key order) |
| `respond.status` | `200` by default |
| `respond.headers` | each header is a string or a list (for a repeated header, such as several `Set-Cookie`) |
| `respond.body` | text, or a YAML structure sent as JSON (with `Content-Type: application/json` inferred) |
| `respond.chance` | fraction of the selected requests that get this response; every one of them by default |
| `latency` | fixed delay (`2s`), one drawn from a range (`{min: 200ms, max: 900ms}`), or either of those with its own frequency (`{fixed: 2s, chance: 0.3}`, `{min, max, chance}`) |
| `drop` | closes the connection with no response: `true` for every selected request, `{chance: 0.05}` for a fraction of them |
| `ttl` | lifetime, counted from when the override was registered |
| `maxApplications` | number of applications before it expires |
| `source` | written by the gateway on learned or derived overrides; points at the exchange it came from |
| `probability` | legacy: still read, now as the default frequency of the effects that declare none. Never written back |

Every frequency is a number between `0.0` and `1.0`, and each one is drawn on its own for every request the override selects, in the fixed order drop, response, delay. An effect that was not drawn is ignored as if it were not declared; a request where none was drawn goes to the upstream untouched.

When more than one override matches, the most specific wins: exact path; then path with segment parameters (among those, the one with more literal segments); then regular expression; then wildcard, longest first; and, on a tie, the one declaring more criteria. Anything still tied is settled by the order in the document. Overrides that are off or expired stay out of that contest.

## Environment variables

Precedence is **environment, then `gateway.json`, then the default**. A value coming from the environment is locked: the API and the panel refuse to change it (`409 locked`, naming the variable). `GET /api/settings` shows the effective origin of every value, so "why is it using memory when I configured SQLite?" is one command away.

| Variable | Key in `gateway.json` | Values |
|---|---|---|
| `GATEWAY_CONFIG` | (none) | path to `gateway.json`; `./gateway.json` by default. The `-config` flag does the same |
| `GATEWAY_TRAFFIC_PORT` | `ports.traffic` | integer |
| `GATEWAY_ADMIN_PORT` | `ports.admin` | integer |
| `GATEWAY_SEED` | `seed` | non-negative integer |
| `GATEWAY_HISTORY_BACKEND` | `history.backend` | `memory`, `ndjson` or `sqlite` |
| `GATEWAY_HISTORY_PATH` | `history.path` | file path (relative to the working directory) |
| `GATEWAY_HISTORY_CAPACITY` | `history.capacity` | integer |
| `GATEWAY_HISTORY_RECORD` | `history.record` | `true` or `false` |
| `GATEWAY_HISTORY_EXPOSE` | `history.expose` | `true` or `false` |
| `GATEWAY_CAPTURE_MAX_BODY_BYTES` | `capture.maxBodyBytes` | integer, in bytes |
| `GATEWAY_LEARNING` | `learning.enabled` | `true` or `false` |
| `GATEWAY_ROUTES_DIR` | `routesDir` | directory (relative to the working directory) |

An empty variable counts as absent. The usual case is the history backend differing per machine while the routes stay the same:

```sh
GATEWAY_HISTORY_BACKEND=sqlite GATEWAY_HISTORY_PATH=ci-history.db devgateway
```

## The override: mocking and chaos are the same thing

There is no mocking mechanism and a separate chaos mechanism. There is the override: a selection criterion, a declared response and the modifiers `latency`, `drop`, `ttl` and `maxApplications`, each effect with its own frequency.

- **A mock** is an override whose response declares no frequency (or declares `chance: 1.0`): every selected request gets the declared response.
- **Chaos** is the same override with `respond.chance: 0.3`: 30% get the declared response, and the other 70% go to the upstream as if the override did not exist.
- **Latency only**: with no `respond`, the override does not intercept; the response comes from the upstream, late.
- **A drop**: `drop: true` closes the connection with no response, and `drop: {chance: 0.05}` does it on one call in twenty.
- **One rule, several frequencies**: "fails in 30% of the calls and is always slow" is a `respond.chance: 0.3` next to a `latency` with no frequency — not two overrides on the same path.

The route forwards everything by default; overrides intercept only what they select. In the fixed order of the request path, the gateway draws each declared effect against its own frequency — drop, response, delay — and only then answers (synthesizing, or going to the upstream). The delay is applied with the response ready, just before writing it: that is why injected time and upstream time are measured separately and **add up**. In the history, every exchange carries `timing.upstreamMs`, `timing.injectedMs` and `timing.gatewayMs`, which the panel draws as a waterfall.

With a `seed` set, each request's draw derives from `(seed, arrival sequence number)`: the same sequence of requests produces the same decisions in another run.

The API can also build an override out of a captured exchange (`POST /api/routes/{route}/overrides/derive`), with the real response prefilled; in the panel, that is the "create override" button on an open exchange.

## The panel

The panel is served on the admin port, embedded in the binary, with no CDN and no network access of its own. It is a client of the documented API and nothing more — anything it does, you can do with `curl`.

- **Map**: the routes and their upstreams, with recent availability of each upstream, taken from the forwarding attempts themselves (the gateway does not probe anything).
- **Traffic**: the exchanges as they happen, over a live stream, with filters (route, method, path, status range, intervened or not, time window) and per-exchange detail: request and response, headers, bodies, and the waterfall splitting upstream time from injected time.
- **Route**: the route, its overrides, and the continuous controls (frequency, latency, TTL, applications) next to the YAML document, live and editable. What you change in the controls shows up in the document, and the other way around.
- **Process**: the effective settings with the origin of each value, learning mode, and the history backend, all changeable without a restart.

The detail pane has a simple and an advanced mode. Simple is the default and fits a rule into two lines; a rule using anything beyond path and method is flagged, so nothing is hidden without saying so. Advanced shows everything.

## Learning mode

With `learning.enabled` on, every new combination of method and path that goes through a route and is answered by the upstream becomes a **disabled** override in the route's document, carrying the observed status, headers and body, plus a `source` block pointing at the exchange it came from:

```yaml
  - name: get-catalog-products-p3
    enabled: false
    match:
      path: /catalog/products/p3
      method: GET
    respond:
      status: 200
      headers:
        Content-Type: application/json
      body:
        id: p3
        name: Mechanical pencil 0.5 mm
    source:
      kind: learned
      exchange: 01M2V1XH0K450DW9ZKFEPGG80J
      at: 2026-09-18T20:04:24.72Z
```

Being off, it changes no traffic. It is the starting point for the next move: turn it on, edit the response, or give it a failure frequency. Learning writes after the response has been delivered, off the request path.

The recorded path is generalized: a segment that looks like a record identifier — all digits, a UUID, or alphanumeric with digits and at least 8 characters — becomes a segment parameter (`:id`, `:id2`…), and the rest stays literal. `GET /zip/40415345/json` followed by `GET /zip/01001000/json` produce a single override, `get-zip-id-json`, with path `/zip/:id/json` and the first exchange's response; `/api/users/me` and `/api/users/42` produce two, `/api/users/me` and `/api/users/:id`, because `me` is not an identifier. The heuristic is conservative: `json`, `charge` and `ch_123` stay literal, and an identifier it does not recognize (a slug, say) only costs one extra rule, which you generalize by replacing the segment with `:id`.

A combination counts as known when the route has an override, on or off, for the same method with the same generalized path, or whose path (exact or parameterized) matches the request; wildcards and regular expressions do not count, so the endpoints under them get learned too. When a generalized override is written, the learned exact-path ones it covers — still disabled and with no other criteria — are replaced by it, in the same spot in the document. A learned override you turned on or narrowed stays, and keeps winning over the generalized one.

Turn it on and off without restarting:

```sh
curl -s -X PUT localhost:8081/api/learning -H 'Content-Type: application/json' -d '{"enabled":true}'
```

The mode applies to every route. See the [limits](#assumed-limits) on paths with identifiers and on comments.

## Hot reconfiguration

Nothing requires restarting the process:

- **Routes and overrides**: every write through the API or the panel validates, writes the document and applies it. After editing the files by hand, `curl -s -X POST localhost:8081/api/reload` rereads everything. An invalid document is refused, and the previous configuration stays in force.
- **The process, ports and history backend included**: `PATCH /api/settings` changes `gateway.json` and applies it:

  ```sh
  curl -s -X PATCH localhost:8081/api/settings \
    -H 'Content-Type: application/merge-patch+json' -d '{"ports":{"traffic":9080}}'
  ```

  The new port is opened before the old one closes. If it cannot be opened, nothing changes (`409 port_unavailable`). The old port stops accepting connections and finishes the requests in flight. Switching the history backend follows the same protocol.
- Requests in flight finish under the configuration they started with; the configuration swap is atomic.

## The API

Everything you configure in the files you can also configure through the API, and the panel is just a client of that API. Next to the controls, the panel shows the matching YAML or JSON document, live and editable.

| In the file | In the API |
|---|---|
| create `routes/x.yaml` | `POST /api/routes` or `PUT /api/routes/x/document` |
| edit a field of the route | `PATCH /api/routes/x` |
| edit the YAML by hand | `PUT /api/routes/x/document` |
| delete `routes/x.yaml` | `DELETE /api/routes/x` |
| add, edit or remove an override | `POST`, `PATCH` and `DELETE /api/routes/x/overrides[/y]` |
| `enabled: false` on an override | `PATCH /api/routes/x/overrides/y` with `{"enabled": false}` |
| edit `gateway.json` | `PATCH /api/settings` or `PUT /api/settings/document` |
| `learning.enabled` | `PUT /api/learning` |
| edit the files outside the panel | `POST /api/reload` |

A write through the API rewrites **only** the touched route's document, atomically; the others stay byte for byte the same.

The full reference — every endpoint, the live SSE stream, the error codes and a `curl` example per operation — is in [`docs/api.md`](docs/api.md).

## Example environment

To see the whole thing working without services of your own, [`examples/`](examples) has two toy services, ready-made routes and two scripts: `examples/run.sh` brings the environment up, and `examples/e2e.sh` checks it end to end. `run.sh` works on a copy in `examples/.run/` and keeps it between runs, along with the services and rules you create; `CLEAN=1 examples/run.sh` starts over from the original example, moving the previous copy to a `.bak-<date>` directory.

## Development

```sh
make test          # go test ./...
make race          # tests with -race (needs CGO and a C compiler)
make lint          # gofmt and go vet
make build         # bin/devgateway with the current web/dist
make web           # builds the panel into web/dist
make web-clean     # restores web/dist to the committed placeholder
make release       # panel + per-platform binaries in dist/
make docker        # Docker image
```

- Go code in `cmd/devgateway` and `internal/`; the panel in `web/` (Vite, React and TypeScript; see [`web/README.md`](web/README.md)).
- The committed `web/dist/index.html` is a placeholder, so that `go build` works in a clone without Node. After `make web`, do not commit it; `make web-clean` restores it.
- Without a C toolchain (`make race` needs one), the race detector runs in a container:

  ```sh
  docker run --rm -v "$PWD:/src" -w /src golang:1.26 go test -race -count=1 ./...
  ```

- CI runs `make build`, `make lint` and `make race` on every push and pull request.

## Assumed limits

Deliberate decisions, with the cost in plain sight:

- **Comments are lost on writes and on learning.** When the API, the panel or learning mode writes a route document, it is reserialized: comments and the original key order of that document are lost. The damage is confined to the route touched, and the panel warns before the first write to a document with comments. If the comments matter, keep the original under version control and work on a copy (that is what `examples/run.sh` does).
- **Determinism is by arrival order.** The seed pins the decisions by sequence number, not by content. Reproducing a run means sending the requests again in the same order; concurrent requests can arrive in a different order from one run to the next, so strict reproducibility calls for sending them serially.
- **Connection drops degrade on HTTP/2.** Over HTTP/1.1 the gateway closes the socket with no response. Over HTTP/2 there is no socket of the request's own, and the drop becomes an abrupt stream cancellation. The captured exchange records which of the two happened in `dropMode` (`hijack`, `stream_reset`, or `abort` when the HTTP/1.x connection cannot be hijacked).
- **Identifiers the heuristic does not recognize produce one override per value.** Learning generalizes digits, UUIDs and alphanumeric codes with digits, but a slug like `/posts/my-title` or a short code like `ch_123` becomes one override per value. Replace the segment with `:id` in one of them and delete the rest; from then on the values it covers are known and produce no new rules. `GET /api/learning` counts the learned overrides per route.
- **Switching the history backend does not migrate the exchanges.** Earlier exchanges stay in the old backend, intact (the NDJSON or SQLite file is still on disk); the new one starts empty. The API response and the panel say so at the moment of the switch.

Also out of scope for now: TLS on the traffic port (the gateway speaks HTTP to the client and HTTP or HTTPS to the upstream), authentication on the admin port (the intended use is local) and automatic retention in the persistent backends.

## License

MIT. Copyright (c) joaovillas. See [LICENSE](LICENSE).

How to contribute: [CONTRIBUTING.md](CONTRIBUTING.md). Reporting a vulnerability: [SECURITY.md](SECURITY.md). Expected conduct: [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
