# Admin API

The contract of the REST API served on the admin port (`8081` by default), under the `/api/` prefix. The backend implements it in `internal/admin`. The panel in `web/` is a client of this API and nothing else: every operation the panel performs has a runnable `curl` equivalent here.

Everything this document describes is implemented.

The JSON fields are those of the Go types in `internal/config` (`Route`, `Override`, `EffectiveValue`) and in `internal/exchange` (`Exchange`, `Filter`). Where this document and the code disagree, the types in the code win and this document gets fixed.

In the examples, `$A` is `http://localhost:8081`.

## Summary

| Group | Method and path | Operation |
|---|---|---|
| Process | `GET /api/status` | process state at a glance |
| Routes | `GET /api/routes` | list routes |
| | `GET /api/routes/{route}` | read one route |
| | `POST /api/routes` | create a route |
| | `PUT /api/routes/{route}` | replace a route |
| | `PATCH /api/routes/{route}` | change fields of a route |
| | `DELETE /api/routes/{route}` | remove a route and its document |
| | `GET /api/routes/{route}/document` | read the raw YAML |
| | `PUT /api/routes/{route}/document` | write the raw YAML |
| Overrides | `GET /api/routes/{route}/overrides` | list the route's overrides |
| | `GET /api/routes/{route}/overrides/{override}` | read one override |
| | `POST /api/routes/{route}/overrides` | create an override |
| | `PUT /api/routes/{route}/overrides/{override}` | replace an override |
| | `PATCH /api/routes/{route}/overrides/{override}` | change fields (including on/off) |
| | `DELETE /api/routes/{route}/overrides/{override}` | remove an override |
| | `POST /api/routes/{route}/overrides/{override}/reset` | restart the TTL and the count |
| | `POST /api/routes/{route}/overrides/derive` | derive an override from an exchange |
| | `GET /api/overrides/state` | live state of every override |
| Settings | `GET /api/settings` | effective settings with their origin |
| | `PATCH /api/settings` | change `gateway.json` and apply it live |
| | `GET /api/settings/document` | read the raw `gateway.json` |
| | `PUT /api/settings/document` | write the raw `gateway.json` |
| | `GET /api/learning` | learning mode state |
| | `PUT /api/learning` | turn learning on or off |
| | `POST /api/reload` | reload the files from disk |
| History | `GET /api/exchanges` | list exchanges, with filters and a cursor |
| | `GET /api/exchanges/{id}` | read one full exchange |
| | `GET /api/exchanges/{id}/older` | the previous (older) exchange |
| | `GET /api/exchanges/{id}/newer` | the next (newer) exchange |
| | `DELETE /api/exchanges` | clear the history |
| Upstreams | `GET /api/upstreams` | recent availability per upstream |
| Live | `GET /api/events` | SSE stream |

## Conventions

### Format

- Request and response bodies are `application/json; charset=utf-8`, except the raw documents: `application/yaml` for routes and `application/json` for `gateway.json`.
- Keys are camelCase, the same as in the documents.
- Durations are strings in Go's format: `"150ms"`, `"2s"`, `"1m30s"`.
- Instants are RFC 3339 with a fraction, in UTC: `"2026-09-18T15:04:05.123Z"`.
- Measured times (`timing`, `ttlRemainingMs`) are numbers in milliseconds.
- Route and override names appear in the path as they are, percent-encoded where necessary.
- Captured bodies (`request.body`, `response.body` of an exchange) are `[]byte` in Go and therefore arrive **base64-encoded**. The client decodes them and uses `headers["Content-Type"]` to decide how to display them. The `size` field holds the real size, and `truncated` says the body was cut at the capture limit.

### Errors

Every error response has this body:

```json
{
  "error": "invalid",
  "message": "routes/payments.yaml: field overrides[0].respond.chance (line 12, column 18): must be between 0.0 and 1.0 (got 1.5)",
  "field": "overrides[0].respond.chance",
  "file": "routes/payments.yaml",
  "line": 12,
  "column": 18
}
```

| Field | Presence | Meaning |
|---|---|---|
| `error` | always | stable code, for the client to decide what to do |
| `message` | always | human-readable text, ready to display as is |
| `field` | when a field is responsible | path of the field (`overrides[0].latency.min`, `ports.traffic`) |
| `file` | when a document is responsible | path of the file, or the environment variable that holds the value |
| `line`, `column` | when the field was located in the document | 1-based position |
| `env` | only on `locked` | the environment variable that locks the value |
| `errors` | when there is more than one problem | list of objects with `message`, `field`, `file`, `line`, `column` |

The codes in use:

| HTTP | `error` | When |
|---|---|---|
| 400 | `bad_request` | malformed JSON, invalid query parameter, missing body |
| 400 | `bad_cursor` | pagination cursor not issued by this backend |
| 403 | `history_disabled` | history exposure is off (`history.expose = false`) |
| 404 | `not_found` | no such route, override, exchange or API resource |
| 404 | `no_more` | item-by-item navigation reached the end in that direction |
| 405 | `method_not_allowed` | method not supported on that path |
| 409 | `conflict` | name already taken, or the same match as another route |
| 409 | `locked` | value set by an environment variable |
| 409 | `port_unavailable` | the new port could not be opened; the current one stays in use |
| 409 | `backend_unavailable` | the new history backend did not initialize; the current one stays in use |
| 412 | `stale` | `If-Match` does not agree with the document's current version |
| 415 | `unsupported_media_type` | `Content-Type` other than the expected one |
| 422 | `invalid` | validation failed; nothing was written |
| 500 | `internal` | unexpected failure, including a failed disk write |

`history_disabled` is deliberately distinct from an empty list: the panel says "history disabled" in one case and "no exchanges yet" in the other.

### Writes and documents

- Every write validates first. If validation fails, the response is `422 invalid` and no file is touched.
- A route or override write rewrites **only** that route's document, atomically (temporary file and rename), under a write mutex. The other documents stay byte for byte the same.
- The rewrite loses the comments and the key order of the document it touched. That is why reading a route reports `hasComments`, and why the panel warns before the first write.
- Every document has an opaque version, returned in the `ETag` header. Every write accepts an optional `If-Match`. With it, a diverging version answers `412 stale` and writes nothing. Without it, the last write wins.
- Every successful write rebuilds the snapshot, applies the configuration live and emits a `config` event on the SSE stream.

---

## Process

### `GET /api/status`

State at a glance, used by the panel's top bar. It is also the body of the SSE stream's `hello` event.

```json
{
  "version": "0.1.0",
  "schemaVersion": 1,
  "startedAt": "2026-09-18T15:00:00Z",
  "configPath": "gateway.json",
  "routesDir": "routes",
  "ports": { "traffic": 8080, "admin": 8081 },
  "history": { "backend": "memory", "record": true, "expose": true },
  "learning": { "enabled": false },
  "routes": 3
}
```

- `ports` are the ports the process is listening on right now. With port `0` in the configuration, which asks the system for a free port, the chosen port shows up here.
- `history.backend` is the backend in use, which changes with a live switch.

```sh
curl -s $A/api/status
```

---

## Routes

### The route resource

Reading a route wraps the document (`route`, identical to the `config.Route` type) in metadata that does not belong to the document.

```json
{
  "file": "routes/payments.yaml",
  "hasComments": true,
  "order": 0,
  "route": {
    "schemaVersion": 1,
    "name": "payments",
    "upstream": "http://localhost:9001",
    "match": { "host": "", "path": "/api/payments/*" },
    "stripPrefix": false,
    "rewriteHost": false,
    "timeout": "5s",
    "overrides": [
      {
        "name": "flaky",
        "enabled": true,
        "match": { "path": "/api/payments/charge", "method": "POST" },
        "respond": {
          "status": 503,
          "headers": { "Retry-After": "1" },
          "body": { "error": "unavailable" },
          "chance": 0.3
        },
        "latency": { "min": "100ms", "max": "500ms" },
        "ttl": "10m",
        "maxApplications": 50
      }
    ]
  },
  "state": {
    "flaky": {
      "active": true,
      "expired": null,
      "registeredAt": "2026-09-18T15:02:00Z",
      "ttlRemainingMs": 412000,
      "applications": 7,
      "maxApplications": 50,
      "lastAppliedAt": "2026-09-18T15:05:10.200Z"
    }
  }
}
```

- `order` is the route's position in the precedence order (0 is the most specific).
- `state` is the live state of each override, by name (see [live state](#get-apioverridesstate)).
- Omitted fields follow the document's rules: an absent `enabled` means on, an absent frequency means the effect holds on every selected request, an absent `respond.status` means `200`.
- `match.path` is exact (`/zip/01001000/json`), parameterized (`/zip/:id/json`, where each `:name` matches exactly one non-empty segment) or a suffix wildcard (`/zip/*`); `match.pathRegex` is the regular-expression alternative. A parameter name follows `[A-Za-z_][A-Za-z0-9_]*`, does not repeat within a path and does not share a segment with the wildcard; the error comes back as `422` with `field: "match.path"`. A route's `match.path` does not take parameters. In the precedence order, a parameterized path comes after an exact path and before a regular expression and a wildcard, and between two parameterized paths the one with more literal segments wins.
- `latency` is either a string (`"2s"`, a fixed delay), `{ "min", "max" }` (a delay drawn from the range) or the long form with its own frequency: `{ "fixed": "2s", "chance": 0.3 }` or `{ "min", "max", "chance" }`.
- `drop` is either `true` (every selected request) or `{ "chance": 0.05 }`; `false` is the same as not declaring it.
- Each effect carries its own frequency, between `0.0` and `1.0`, drawn independently on every selected request: `respond.chance`, `latency.chance` and `drop.chance`. An effect that declares none holds always. A frequency outside the range answers `422` with `field` pointing at it (`overrides[0].respond.chance`).
- `probability` is legacy: still accepted, it now works as the default frequency of the effects that declare none. New writes do not produce it.
- A `headers`, `query` or `body` criterion is either a string (equality) or an object with exactly one of `equals`, `regex`, `json`, `contains`.
- `source` shows up on learned or derived overrides: `{ "kind": "learned" | "derived", "exchange": "<id>", "at": "<instant>", "bodyIncomplete": true }`.

### `GET /api/routes`

Lists the routes in precedence order.

```json
{ "items": [ { "file": "routes/payments.yaml", "hasComments": false, "order": 0, "route": { "...": "..." }, "state": { } } ] }
```

With no routes, `items` is `[]`.

```sh
curl -s $A/api/routes
```

### `GET /api/routes/{route}`

Returns the route resource and the document's `ETag` header.

Errors: `404 not_found`.

```sh
curl -si $A/api/routes/payments
```

### `POST /api/routes`

Creates the route and the document `routes/{name}.yaml`. The body is a `config.Route`. An absent `schemaVersion` takes the binary's version.

A renamed route keeps its original file (see `PUT /api/routes/{route}`), so `routes/{name}.yaml` may already be another route's document. In that case the new route goes to the first free `routes/{name}-N.yaml`, starting at `N = 2`, and the resource's `file` field says where. An existing file is never overwritten. If `routes/{name}.yaml` exists but is not the document of any route in force, creation answers `409 conflict`. The same rule applies to creation through `PUT /api/routes/{route}/document`.

```json
{
  "name": "orders",
  "upstream": "http://localhost:9002",
  "match": { "path": "/api/orders/*" },
  "timeout": "3s"
}
```

Answers `201 Created` with the route resource, `Location: /api/routes/orders` and an `ETag`.

Errors: `400 bad_request`, `409 conflict` (name already taken, or the same host and path as another route; the message names the conflicting file), `422 invalid`.

```sh
curl -s -X POST $A/api/routes -H 'Content-Type: application/json' \
  -d '{"name":"orders","upstream":"http://localhost:9002","match":{"path":"/api/orders/*"},"timeout":"3s"}'
```

### `PUT /api/routes/{route}`

Replaces the whole route, overrides included. A `name` different from the one in the path renames the route, and the document keeps its current file path. Overrides kept under the same name keep their live state (TTL and count).

Answers `200` with the route resource. Errors: `400`, `404 not_found`, `409 conflict`, `412 stale`, `422 invalid`.

```sh
curl -s -X PUT $A/api/routes/orders -H 'Content-Type: application/json' \
  -d '{"schemaVersion":1,"name":"orders","upstream":"http://localhost:9002","match":{"path":"/api/orders/*"},"timeout":"5s"}'
```

### `PATCH /api/routes/{route}`

Changes fields of the route with JSON Merge Patch (RFC 7396): the keys present replace the current ones, and `null` removes the field from the document. `overrides` is not accepted here (`422`), because overrides have endpoints of their own.

```json
{ "upstream": "http://localhost:9003", "timeout": null }
```

Answers `200` with the route resource. Errors: `400`, `404`, `409 conflict`, `412`, `422`.

```sh
curl -s -X PATCH $A/api/routes/orders -H 'Content-Type: application/merge-patch+json' \
  -d '{"upstream":"http://localhost:9003","timeout":null}'
```

### `DELETE /api/routes/{route}`

Removes the route and deletes the document. Answers `204`. Errors: `404`, `412`.

```sh
curl -s -X DELETE $A/api/routes/orders
```

### `GET /api/routes/{route}/document`

Returns the YAML document exactly as it is on disk, comments included, with `Content-Type: application/yaml; charset=utf-8` and an `ETag`.

```yaml
# Payments: the billing team's local service
schemaVersion: 1
name: payments
upstream: http://localhost:9001
match:
  path: /api/payments/*
overrides:
  - name: flaky
    match:
      path: /api/payments/charge
      method: POST
    respond:
      status: 503
      chance: 0.3
    latency:
      fixed: 2s
```

Errors: `404`.

```sh
curl -s $A/api/routes/payments/document
```

### `PUT /api/routes/{route}/document`

Writes the raw document. The body is YAML (`Content-Type: application/yaml`). The text is validated as it would be when loaded from disk and written **as sent**, comments included. If the route does not exist, it is created (`201`). The declared `name` has to equal the `{route}` in the path, otherwise `422` with `field: "name"`.

Answers `200` or `201` with the route resource and the new `ETag`. Errors: `409 conflict`, `412 stale`, `415`, `422 invalid` with `file`, `field`, `line` and `column` pointing at the problem.

```sh
curl -s -X PUT $A/api/routes/payments/document -H 'Content-Type: application/yaml' \
  --data-binary @routes/payments.yaml
```

---

## Overrides

### The override resource

```json
{
  "route": "payments",
  "order": 0,
  "override": {
    "name": "flaky",
    "enabled": true,
    "match": { "path": "/api/payments/charge", "method": "POST" },
    "respond": { "status": 503, "chance": 0.3 },
    "latency": "2s",
    "drop": { "chance": 0.05 },
    "ttl": "60s",
    "maxApplications": 5
  },
  "state": {
    "active": true,
    "expired": null,
    "registeredAt": "2026-09-18T15:02:00Z",
    "ttlRemainingMs": 40000,
    "applications": 2,
    "maxApplications": 5,
    "lastAppliedAt": "2026-09-18T15:02:19.800Z"
  }
}
```

`override` is the `config.Override` type. `order` is the override's position in the precedence order within the route. `state` is described under [live state](#get-apioverridesstate).

Each declared effect carries its own frequency, and the three are drawn independently on every request the override selects: this one synthesizes the `503` in 30% of them, delays every one of them by `2s` and drops 5% of the connections. An effect that declares no `chance` holds always; an effect that was not drawn is ignored as if it were not declared, and a request where none was drawn goes to the upstream untouched. An undeclared effect does not show up in the JSON: `latency` and `drop` are absent, not `null`. The legacy `probability` field is still read, as the default frequency of the effects that declare none, and is never written back.

### `GET /api/routes/{route}/overrides`

```json
{ "items": [ { "route": "payments", "order": 0, "override": { "...": "..." }, "state": { "...": "..." } } ] }
```

Errors: `404` (route).

```sh
curl -s $A/api/routes/payments/overrides
```

### `GET /api/routes/{route}/overrides/{override}`

Errors: `404` (route or override).

```sh
curl -s $A/api/routes/payments/overrides/flaky
```

### `POST /api/routes/{route}/overrides`

Appends an override to the end of the route's declared list. The body is a `config.Override`. Answers `201` with the override resource. Errors: `404` (route), `409 conflict` (name already used in the route), `412`, `422`.

```sh
curl -s -X POST $A/api/routes/payments/overrides -H 'Content-Type: application/json' \
  -d '{"name":"flaky","match":{"path":"/api/payments/charge","method":"POST"},"respond":{"status":503,"chance":0.3},"latency":"2s"}'
```

### `PUT /api/routes/{route}/overrides/{override}`

Replaces the whole override, keeping its position in the declared list. A different `name` renames it. Answers `200`. Errors: `404`, `409`, `412`, `422`.

```sh
curl -s -X PUT $A/api/routes/payments/overrides/flaky -H 'Content-Type: application/json' \
  -d '{"name":"flaky","match":{"path":"/api/payments/charge"},"respond":{"status":500,"chance":0.5}}'
```

### `PATCH /api/routes/{route}/overrides/{override}`

JSON Merge Patch over the override. This is the endpoint behind the continuous controls and the on/off switch. `null` removes a field (`"latency": null`, for instance, takes the delay away).

A frequency lives inside its effect, so the patch that changes one names that effect: `{"respond": {"chance": 0.3}}`, `{"latency": {"fixed": "2s", "chance": 0.5}}`, `{"drop": {"chance": 0.05}}`. `{"drop": false}` takes the drop away, and `{"latency": null}` takes the delay away.

The API does not turn an override on by itself. The spec asks that adjusting the frequency, latency or drop of a disabled override turn it on in the same gesture, and the client is the one who does that, by sending `"enabled": true` along:

```json
{ "respond": { "chance": 0.3 }, "enabled": true }
```

On/off by itself:

```json
{ "enabled": false }
```

Turning it off and back on preserves every other field. Answers `200` with the override resource. Errors: `400`, `404`, `412`, `422`.

```sh
curl -s -X PATCH $A/api/routes/payments/overrides/flaky -H 'Content-Type: application/merge-patch+json' \
  -d '{"respond":{"chance":0.3},"enabled":true}'
curl -s -X PATCH $A/api/routes/payments/overrides/flaky -H 'Content-Type: application/merge-patch+json' \
  -d '{"drop":{"chance":0.05}}'
curl -s -X PATCH $A/api/routes/payments/overrides/flaky -H 'Content-Type: application/merge-patch+json' \
  -d '{"enabled":false}'
```

### `DELETE /api/routes/{route}/overrides/{override}`

Answers `204`. Errors: `404`, `412`.

```sh
curl -s -X DELETE $A/api/routes/payments/overrides/flaky
```

### `POST /api/routes/{route}/overrides/{override}/reset`

Restarts the TTL clock and zeroes the application count, reactivating an expired override. It does not change the document. Answers `200` with the override resource. Errors: `404`.

```sh
curl -s -X POST $A/api/routes/payments/overrides/flaky/reset
```

### `POST /api/routes/{route}/overrides/derive`

Builds an override out of an exchange from the history: an exact-path and method criterion taken from the observed request, and a response with the status, headers and body the upstream returned. The only headers left out are `Date`, `Content-Length`, the gateway's own `X-Gateway` and the hop-by-hop ones (including those named in `Connection`). A repeated header, such as several `Set-Cookie`, becomes a list, in the observed order. `source.kind` is `derived` and `source.exchange` points at the source exchange.

Body:

```json
{ "exchange": "01K5E3V3C8Q2M4Z8N6P0R2T4W6", "name": "charge-ok", "save": false }
```

- `exchange` is required (`422` with `field: "exchange"` without it).
- `name` is optional. Without it, the name is generated from the method and path (`post-api-payments-charge`), with a `-2`, `-3`… suffix if it is already taken in the route.
- `save: false` (the default) returns the draft with `200`, writing nothing: a derived override does not apply before it has been reviewed. This is the review step: the panel shows the draft, the user edits it and creates it with `POST /api/routes/{route}/overrides`.
- `save: true` writes it straight away and answers `201`, like the creating `POST`: the override resource, with `Location` and `ETag`, plus `warnings`. A `name` already used in the route answers `409 conflict`.

The response (a draft):

```json
{
  "route": "payments",
  "override": {
    "name": "charge-ok",
    "match": { "path": "/api/payments/charge", "method": "POST" },
    "respond": {
      "status": 200,
      "headers": { "Content-Type": "application/json", "Set-Cookie": ["a=1", "b=2"] },
      "body": { "id": "ch_1", "status": "paid" }
    },
    "source": {
      "kind": "derived",
      "exchange": "01K5E3V3C8Q2M4Z8N6P0R2T4W6",
      "at": "2026-09-18T15:10:00Z"
    }
  },
  "warnings": []
}
```

A JSON body that is valid, complete and whose numbers survive the round trip becomes a structure in `respond.body`. Any other body becomes text, identical to the observed one. A path that cannot be written as an exact path (it holds a literal `*`, or a segment starting with `:`) becomes an anchored `pathRegex`. Deriving does not generalize the path: it replays one specific exchange.

The draft declares no frequency on any effect, so the derived response holds on every request it selects. Turning it into an intermittent failure is a later edit — `respond.chance`, `latency.chance` or `drop.chance` — through the creating `POST` or a `PATCH`.

If the response body was truncated on capture, or the transfer was interrupted, the draft comes out with `source.bodyIncomplete: true` and a warning in `warnings`. Deriving is not refused, and the panel shows the warning before writing. `warnings` also fires when the route already has an override for the same method and path, when the requested `name` is already taken in the route (drafts only), and when the exchange was served by another route.

Errors: `400`, `403 history_disabled`, `404 not_found` (no such route, or no such exchange: `"message": "exchange 01K5... not found in the history"`), `409 conflict` (only with `save`), `412` (only with `save`), `422` (no `exchange`, or an exchange with no upstream response: synthesized, dropped, a gateway error or a protocol upgrade).

The `.../overrides/derive` path is reserved for `POST` only: an override named `derive` is still read, changed and removed through the other methods.

```sh
curl -s -X POST $A/api/routes/payments/overrides/derive -H 'Content-Type: application/json' \
  -d '{"exchange":"01K5E3V3C8Q2M4Z8N6P0R2T4W6","name":"charge-ok"}'
curl -s -X POST $A/api/routes/payments/overrides/derive -H 'Content-Type: application/json' \
  -d '{"exchange":"01K5E3V3C8Q2M4Z8N6P0R2T4W6","name":"charge-ok","save":true}'
```

### `GET /api/overrides/state`

The live state of every override of every route, for the panel's map and counters.

```json
{
  "now": "2026-09-18T15:02:20Z",
  "items": [
    {
      "route": "payments",
      "override": "flaky",
      "enabled": true,
      "active": true,
      "expired": null,
      "registeredAt": "2026-09-18T15:02:00Z",
      "ttlRemainingMs": 40000,
      "applications": 2,
      "maxApplications": 5,
      "lastAppliedAt": "2026-09-18T15:02:19.800Z"
    },
    {
      "route": "payments",
      "override": "charge-learned",
      "enabled": false,
      "active": false,
      "expired": null,
      "registeredAt": "2026-09-18T14:50:00Z",
      "ttlRemainingMs": null,
      "applications": 0,
      "maxApplications": null,
      "lastAppliedAt": null
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `enabled` | the value in the document |
| `active` | takes part in the selection right now: on and not expired |
| `expired` | `null`, `"ttl"` or `"applications"` |
| `registeredAt` | when the TTL clock started: creation, last change to `ttl` or `maxApplications`, being turned back on, or a `reset` |
| `ttlRemainingMs` | TTL left, `null` with no TTL, `0` when expired |
| `applications` | applications since `registeredAt` |
| `maxApplications` | the declared limit, `null` with no limit |
| `lastAppliedAt` | last application, `null` if never |

The panel counts `ttlRemainingMs` down locally between events, starting from `now`.

```sh
curl -s $A/api/overrides/state
```

---

## Process settings

### `GET /api/settings`

The effective settings with the origin of every value, in `Settings.Effective()` order. Each item is a `config.EffectiveValue` plus `locked`.

```json
{
  "file": { "path": "gateway.json", "exists": true },
  "values": [
    { "key": "ports.traffic", "env": "GATEWAY_TRAFFIC_PORT", "value": 9090, "source": { "origin": "env", "name": "GATEWAY_TRAFFIC_PORT" }, "locked": true },
    { "key": "ports.admin", "env": "GATEWAY_ADMIN_PORT", "value": 8081, "source": { "origin": "default" }, "locked": false },
    { "key": "seed", "env": "GATEWAY_SEED", "value": 42, "source": { "origin": "file", "name": "gateway.json" }, "locked": false },
    { "key": "history.backend", "env": "GATEWAY_HISTORY_BACKEND", "value": "memory", "source": { "origin": "default" }, "locked": false },
    { "key": "history.path", "env": "GATEWAY_HISTORY_PATH", "value": "", "source": { "origin": "default" }, "locked": false },
    { "key": "history.capacity", "env": "GATEWAY_HISTORY_CAPACITY", "value": 1000, "source": { "origin": "default" }, "locked": false },
    { "key": "history.record", "env": "GATEWAY_HISTORY_RECORD", "value": true, "source": { "origin": "default" }, "locked": false },
    { "key": "history.expose", "env": "GATEWAY_HISTORY_EXPOSE", "value": true, "source": { "origin": "default" }, "locked": false },
    { "key": "capture.maxBodyBytes", "env": "GATEWAY_CAPTURE_MAX_BODY_BYTES", "value": 65536, "source": { "origin": "default" }, "locked": false },
    { "key": "learning.enabled", "env": "GATEWAY_LEARNING", "value": false, "source": { "origin": "default" }, "locked": false },
    { "key": "routesDir", "env": "GATEWAY_ROUTES_DIR", "value": "routes", "source": { "origin": "default" }, "locked": false }
  ]
}
```

- `source.origin` is `env`, `file` or `default`. `source.name` is the variable or the file.
- `locked` is true when `origin` is `env`: the API refuses to change that value.
- `seed` with `value: null` means randomness that does not repeat.

```sh
curl -s $A/api/settings
```

### `PATCH /api/settings`

Changes `gateway.json` with JSON Merge Patch over the file's shape (`config.GatewayFile`) and applies the result live, with no restart. `null` removes the key from the file, and the value goes back to its default. Relative paths (`history.path`, `routesDir`) resolve against the directory of `gateway.json`. The file is rewritten whole, indented with two spaces. If it does not exist yet, it is created, declaring `schemaVersion`.

```json
{ "seed": 42, "ports": { "traffic": 9090 }, "history": { "backend": "sqlite", "path": "data/history.db" } }
```

The order in which it is applied, all under the write mutex (two concurrent changes are serialized, and neither is lost):

1. Refuses with `409 locked` if any key touched comes from the environment, even when the requested value equals the current one. An object replaced by `null` touches every key under it. Nothing is written.
2. Validates the resulting file: its shape (`422 invalid` with `field`, unknown keys included) and its values (`422 invalid`, for instance equal ports or an unknown backend). With an `If-Match` that diverges from the `ETag` of `GET /api/settings/document`, `412 stale`.
3. If the traffic or admin port changes, opens the new listener. If that fails, answers `409 port_unavailable` and nothing changes.
4. If the history backend changes (or its file, or the in-memory backend's capacity), initializes the new one. If that fails, closes what step 3 opened, answers `409 backend_unavailable` and nothing changes.
5. Writes `gateway.json` atomically (temporary file and rename). If that fails, undoes steps 3 and 4 and answers `500`.
6. Swaps: the new history backend takes over and the old one is closed (**the history is not migrated**); the new ports start serving and the old server gets a `Shutdown`, which stops accepting connections and lets the requests in flight finish on it; the new snapshot is published. Seed, history recording and exposure, capture limit, learning and the routes directory take effect for the requests that follow. If `routesDir` changes, the routes are read from the new directory, and an invalid directory gets the change refused back in step 2.

Answers `200`, with the new `ETag` of `gateway.json`:

```json
{
  "settings": { "file": { "path": "gateway.json", "exists": true }, "values": [ "..." ] },
  "applied": ["ports.traffic", "seed", "history.backend", "history.path"],
  "notes": [
    "the traffic port is now 9090; 8080 stopped accepting connections and is finishing the requests in flight",
    "the history is now in sqlite (data/history.db); the earlier exchanges stay in the memory backend and were not migrated"
  ]
}
```

- `settings` is the body of `GET /api/settings` after the change.
- `applied` lists the keys whose effective value (or origin) changed, in `GET /api/settings` order. Empty when the patch changes nothing.
- `notes` explains the side effects: the old port closed, the history not migrated. Empty when there are none.

When the admin port changes, the response goes out through the old port and only then does it close; the SSE stream open on it ends. The client uses `ports.admin` from `settings.values` to reconnect.

Errors:

```json
{
  "error": "locked",
  "message": "ports.traffic comes from environment variable GATEWAY_TRAFFIC_PORT and cannot be changed through the API; gateway.json was left untouched",
  "field": "ports.traffic",
  "env": "GATEWAY_TRAFFIC_PORT"
}
```

```json
{
  "error": "port_unavailable",
  "message": "port 9090 unavailable: bind: address already in use; the traffic port stays on 8080",
  "field": "ports.traffic"
}
```

```json
{
  "error": "backend_unavailable",
  "message": "history backend sqlite at /ro/history.db: permission denied; history stays on memory",
  "field": "history.backend"
}
```

The rest: `400` (malformed JSON, or a patch that is not an object), `412 stale`, `415`, `422 invalid`, `500`.

```sh
curl -s -X PATCH $A/api/settings -H 'Content-Type: application/merge-patch+json' -d '{"seed":42}'
curl -s -X PATCH $A/api/settings -H 'Content-Type: application/merge-patch+json' -d '{"ports":{"traffic":9090}}'
curl -s -X PATCH $A/api/settings -H 'Content-Type: application/merge-patch+json' \
  -d '{"history":{"backend":"sqlite","path":"data/history.db"}}'
curl -s -X PATCH $A/api/settings -H 'Content-Type: application/merge-patch+json' -d '{"seed":null}'
```

### `GET /api/settings/document`

Returns `gateway.json` as it is on disk, with an `ETag` and `X-Gateway-File-Exists: true`. If the file does not exist, answers `200` with `{}` and the header `X-Gateway-File-Exists: false`, with no `ETag`.

```sh
curl -si $A/api/settings/document
```

### `PUT /api/settings/document`

Writes the raw `gateway.json` (`Content-Type: application/json`) and applies it live, with the same order, the same response and the same errors as `PATCH /api/settings`. The text is written as sent, without reformatting.

The environment lock compares the document sent against the one on disk: a locked key whose value changes in the document is refused with `409 locked`. A document that leaves the locked key as it was (absent included), or that merely repeats the value in force, is accepted. Additional errors: `412 stale`, `415`, `422` with `line` and `column` for invalid JSON.

```sh
curl -s -X PUT $A/api/settings/document -H 'Content-Type: application/json' --data-binary @gateway.json
```

### `GET /api/learning`

```json
{
  "enabled": false,
  "source": { "origin": "default" },
  "locked": false,
  "learned": { "payments": 3, "orders": 0 }
}
```

`learned` counts, per route, the overrides with `source.kind = "learned"`, and covers every route, including the ones with none. This is the count the panel shows so the user can consolidate them into a wildcard or a segment parameter.

Learning writes the generalized path: segments that look like an identifier (all digits, a UUID, or alphanumeric with digits and at least 8 characters) become `:id`, `:id2`…, and the name comes from the method and that path (`get-zip-id-json`). A combination counts as known when the route already has an override for the same method, on or off, with the same generalized path or with an exact or parameterized path that matches the request; wildcards and regular expressions do not count. When a generalized override is written, the learned exact-path ones it covers (still disabled and with no other criteria) are replaced by it, and the `config` event goes out with `cause: "learning"`.

```sh
curl -s $A/api/learning
```

### `PUT /api/learning`

A shortcut for `PATCH /api/settings` with `{"learning":{"enabled":...}}`, written into `gateway.json` and applied live: the next request is already being learned (or already is not).

```json
{ "enabled": true }
```

Answers `200` with the same body as `GET /api/learning`. Errors: `409 locked` (`env: "GATEWAY_LEARNING"`), `412`, `422` (no `enabled`).

```sh
curl -s -X PUT $A/api/learning -H 'Content-Type: application/json' -d '{"enabled":true}'
curl -s -X PUT $A/api/learning -H 'Content-Type: application/json' -d '{"enabled":false}'
```

### `POST /api/reload`

Rereads `gateway.json` and the routes directory and applies them live, under the same rules as `PATCH /api/settings` for ports and backend: a port changed in the file is opened and starts serving, and a changed backend is initialized before the swap. If anything fails, the previous configuration stays in force, and requests in flight finish under the configuration they started with.

Answers `200`:

```json
{
  "routes": 4,
  "changed": { "routes": ["orders"], "settings": ["seed"] },
  "warnings": ["routes directory routes holds no .yaml documents; starting with no routes"]
}
```

Errors: `409 port_unavailable`, `409 backend_unavailable`, `409 conflict` (a collision between documents, naming both files), `422 invalid` (with `errors` when there is more than one problem).

```sh
curl -s -X POST $A/api/reload
```

---

## History

With `history.expose = false`, every read endpoint in this section answers `403 history_disabled`:

```json
{
  "error": "history_disabled",
  "message": "the history is disabled: history.expose = false (origin: environment variable GATEWAY_HISTORY_EXPOSE)",
  "field": "history.expose",
  "env": "GATEWAY_HISTORY_EXPOSE"
}
```

`env` shows up when the value comes from the environment, and `file` when it comes from `gateway.json`.

### The exchange resource

It is the `exchange.Exchange` type:

```json
{
  "id": "01K5E3V3C8Q2M4Z8N6P0R2T4W6",
  "seq": 128,
  "start": "2026-09-18T15:05:10.050Z",
  "method": "POST",
  "host": "localhost:8080",
  "path": "/api/payments/charge",
  "query": "retry=1",
  "clientAddr": "127.0.0.1:53122",
  "route": "payments",
  "upstream": "http://localhost:9001",
  "override": "payments/flaky",
  "interventions": ["synthesized", "delayed"],
  "outcome": "synthesized",
  "status": 503,
  "request": {
    "headers": { "Content-Type": ["application/json"] },
    "body": "eyJhbW91bnQiOjEwMH0=",
    "size": 14
  },
  "response": {
    "headers": { "Content-Type": ["application/json"], "X-Gateway": ["route=payments; override=payments/flaky; intervention=synthesized"] },
    "body": "eyJlcnJvciI6InVuYXZhaWxhYmxlIn0=",
    "size": 23
  },
  "timing": { "totalMs": 2001.4, "upstreamMs": 0, "injectedMs": 2000, "gatewayMs": 1.4 }
}
```

- `outcome`: `upstream`, `synthesized`, `dropped` or `gateway` (an error from the gateway itself: `404` with no route, `502`, `504`, `501`).
- `interventions`: what the override did (`synthesized`, `delayed`, `dropped`). Empty or absent when there was no intervention.
- `dropMode`: `hijack` (HTTP/1.1), `stream_reset` (HTTP/2) or `abort` (HTTP/1.x where hijacking is not possible), only on drops.
- `error`: the text of the upstream's or the gateway's error, when there was one.
- `status`: zero on a drop.
- `headers`: an `http.Header`, that is, each name maps to a list of values.
- `body`: base64. Absent from the listing, which returns the summary without bodies.

### `GET /api/exchanges`

Lists in reverse chronological order, newest exchange first, without the bodies.

Query parameters (all optional and conjunctive, mirroring `exchange.Filter`):

| Parameter | Example | Filter |
|---|---|---|
| `route` | `payments` | matched route, equality |
| `upstream` | `http://localhost:9001` | upstream, equality |
| `override` | `payments/flaky` | the override responsible, equality |
| `method` | `POST` | method, case-insensitive |
| `path` | `/charge` | path contains the text |
| `statusMin`, `statusMax` | `500`, `599` | inclusive status range |
| `intervened` | `true` or `false` | with or without an intervention |
| `since`, `until` | RFC 3339 | the window `[since, until)` |
| `limit` | `50` | page size, 50 by default and 500 at most |
| `cursor` | the value of `next` | continues the previous page |

Answers `200`:

```json
{
  "items": [ { "id": "01K5E3V3C8Q2M4Z8N6P0R2T4W6", "seq": 128, "...": "summary without body" } ],
  "next": "eyJzZXEiOjc4fQ",
  "recording": true,
  "backend": "memory"
}
```

- An empty `next` means there are no more pages.
- `recording: false` means recording is off (`history.record = false`). The list may be empty just because of that, and the panel says so.

Errors: `400 bad_request` (invalid parameter, with `field`), `400 bad_cursor`, `403 history_disabled`.

```sh
curl -s "$A/api/exchanges?route=payments&statusMin=500&statusMax=599&intervened=true&limit=50"
curl -s "$A/api/exchanges?cursor=eyJzZXEiOjc4fQ"
```

### `GET /api/exchanges/{id}`

The full exchange, bodies included. Errors: `403`, `404 not_found`.

```sh
curl -s $A/api/exchanges/01K5E3V3C8Q2M4Z8N6P0R2T4W6
```

### `GET /api/exchanges/{id}/older` and `GET /api/exchanges/{id}/newer`

Item-by-item navigation: returns the full exchange immediately older (`older`) or newer (`newer`) than `{id}`, among those that satisfy the filters in the query. The filters are the same as for `GET /api/exchanges`, except `limit` and `cursor`. Exchange `{id}` itself does not have to satisfy the filter.

Errors: `403`, `404 not_found` (no such `{id}`), `404 no_more` (the end of the history in that direction, with `"message": "no older exchange matches this filter"`).

```sh
curl -s "$A/api/exchanges/01K5E3V3C8Q2M4Z8N6P0R2T4W6/older?statusMin=500&statusMax=599"
curl -s "$A/api/exchanges/01K5E3V3C8Q2M4Z8N6P0R2T4W6/newer?route=payments"
```

### `DELETE /api/exchanges`

Empties the history in the backend in use. The exchanges that follow are recorded as usual. It works with exposure turned off too, because it returns no data. Answers `204` and a `history` event on the SSE stream.

```sh
curl -s -X DELETE $A/api/exchanges
```

---

## Upstreams

### `GET /api/upstreams`

Recent availability of each upstream, for the map. The gateway does not probe the upstreams: the state comes from the most recent forwarding attempts, counted on the request path and independent of history recording.

```json
{
  "items": [
    {
      "upstream": "http://localhost:9001",
      "routes": ["payments"],
      "status": "up",
      "recent": { "attempts": 20, "failures": 0 },
      "lastSuccessAt": "2026-09-18T15:05:10.100Z",
      "lastFailureAt": null,
      "lastError": ""
    },
    {
      "upstream": "http://localhost:9002",
      "routes": ["orders", "orders-admin"],
      "status": "down",
      "recent": { "attempts": 5, "failures": 5 },
      "lastSuccessAt": "2026-09-18T14:40:00Z",
      "lastFailureAt": "2026-09-18T15:05:09Z",
      "lastError": "dial tcp 127.0.0.1:9002: connect: connection refused"
    }
  ]
}
```

- `status`: `down` when the three most recent attempts got no response from the upstream (connection refused, connection timeout, or the route's `timeout` running out before the response, which the gateway returns as a `504`); `unknown` when there has been no attempt since startup or since the upstream was first declared (a reload or write that takes the upstream out of every route discards what was counted for it); `up` otherwise. With fewer than three attempts the status is `up`, and the failures show up in `recent`.
- `recent` covers the last 20 attempts.
- A `5xx` from the upstream itself, including a `504` written by it, does **not** make it `down`: it answered. A client giving up does not count as an attempt either.
- The items come in route precedence order, and `routes` lists the routes pointing at that upstream.
- Routes with no `upstream` do not show up here.

```sh
curl -s $A/api/upstreams
```

---

## Live

### `GET /api/events`

A `text/event-stream` stream. The panel keeps one connection open and reacts to the events. Each event has an `event:` and a `data:` holding JSON on a single line. The server does not resend missed events: on reconnect the client gets `hello` and reloads what it displays over REST.

| `event` | When | `data` |
|---|---|---|
| `hello` | right after connecting | the same body as `GET /api/status` |
| `exchanges` | at most once a second, if there were new exchanges | `{ "items": [summary...], "dropped": 0 }` |
| `config` | after any write, reload or learning | `{ "cause": "api" \| "reload" \| "learning", "routes": ["payments"], "settings": ["seed"] }` |
| `overrides` | when live state changes discretely (appeared, disappeared, turned on, turned off, expired, reactivated, applied), at most once a second | the same body as `GET /api/overrides/state` |
| `upstreams` | when some upstream's `status` changes, checked at most once a second; the `recent` counts changing is not enough | the same body as `GET /api/upstreams` |
| `history` | cleared or backend switched | `{ "cause": "cleared" \| "backend", "backend": "sqlite" }` |
| `heartbeat` | every 15 s | `{ "now": "2026-09-18T15:05:15Z" }` |

- `exchanges.items` comes in recording order (oldest first), without bodies, at most 200 per event; in an interval with more exchanges, the newest 200 are the ones kept. `dropped` counts the exchanges from that interval that were left out, including those lost to a client that did not read in time.
- With history exposure off, `exchanges` is not emitted.
- `config.routes` and `config.settings` are always lists (empty when nothing changed on that side). A change through the API that changes nothing emits no event; a reload always emits one.
- `overrides` does not go out just because `ttlRemainingMs` went down: the panel counts the remainder down locally, starting from `now`.
- The client treats the connection as lost after 45 s with no event at all, since `heartbeat` guarantees one every 15 s.

What the server guarantees:

- A slow client never slows the proxy down. Exchanges reach the stream through a queue that never waits on a subscriber: what does not fit is dropped and counted in `dropped`. Each write to the stream has a 10 s deadline; a client that does not read within it is disconnected and reconnects when it can.
- A connection with no traffic stays open indefinitely, kept alive by the `heartbeat`.
- The stream ends when the admin port goes out of service (a port switch or the process shutting down), so it does not hold the graceful `Shutdown` back. The client reconnects on the new port.

An example of the stream:

```text
event: hello
data: {"version":"0.1.0","schemaVersion":1,"startedAt":"2026-09-18T15:00:00Z","configPath":"gateway.json","routesDir":"routes","ports":{"traffic":8080,"admin":8081},"history":{"backend":"memory","record":true,"expose":true},"learning":{"enabled":false},"routes":3}

event: exchanges
data: {"items":[{"id":"01K5E3V3C8Q2M4Z8N6P0R2T4W6","seq":128,"start":"2026-09-18T15:05:10.050Z","method":"POST","host":"localhost:8080","path":"/api/payments/charge","route":"payments","override":"payments/flaky","interventions":["synthesized"],"outcome":"synthesized","status":503,"request":{"size":14},"response":{"size":23},"timing":{"totalMs":2001.4,"upstreamMs":0,"injectedMs":2000,"gatewayMs":1.4}}],"dropped":0}

event: heartbeat
data: {"now":"2026-09-18T15:05:15Z"}
```

```sh
curl -sN $A/api/events
```

---

## Parity with the files

| In the file | In the API |
|---|---|
| create `routes/x.yaml` | `POST /api/routes` or `PUT /api/routes/x/document` |
| edit a field of the route | `PATCH /api/routes/x` |
| edit the YAML by hand | `PUT /api/routes/x/document` |
| delete `routes/x.yaml` | `DELETE /api/routes/x` |
| add, edit or remove an override | `POST`, `PATCH` and `DELETE /api/routes/x/overrides[/y]` |
| `enabled: false` on an override | `PATCH ... {"enabled": false}` |
| edit `gateway.json` | `PATCH /api/settings` or `PUT /api/settings/document` |
| `learning.enabled` | `PUT /api/learning` |
| edit the files outside the panel | `POST /api/reload` |
