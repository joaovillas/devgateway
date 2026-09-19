## Purpose

Intercepts specific paths inside a route that otherwise just forwards to the upstream, making it possible to force a response, delay it, drop it, or do all of that in only a fraction of the calls — a single mechanism in place of the split between mocking and chaos injection.

## ADDED Requirements

### Requirement: Selective interception with passthrough by default

A route with no overrides SHALL forward every request to its upstream. An override MUST intercept only the requests that satisfy its criteria; all the others MUST go to the upstream with no change in behavior.

#### Scenario: A route with no overrides forwards everything

- **WHEN** a route with the wildcard `/api/payments/*` and no overrides receives requests for three distinct paths
- **THEN** all three are forwarded to the upstream and no response is synthesized

#### Scenario: An override intercepts only what it matches

- **WHEN** an override exists for `/api/payments/bilulu` and requests arrive for `/api/payments/bilulu` and for `/api/payments/charge`
- **THEN** the first is answered by the override without contacting the upstream and the second is forwarded normally

#### Scenario: A route with no upstream and no matching override

- **WHEN** a route declares no upstream and a request arrives that no override intercepts
- **THEN** the gateway responds `501` with a body reporting the route that was hit and the absence of an upstream and of a matching override

### Requirement: Override selection criteria

An override SHALL select requests by path and MAY further restrict by method, headers, query parameters and body. The path MUST accept the exact form, a suffix wildcard, segment parameters (`/zip/:id/json`, where each `:name` matches exactly one non-empty segment) and a regular expression. The remaining criteria MUST accept the operators exact equality, regular expression, JSON equality and substring containment. When an override declares several criteria, all of them MUST match.

#### Scenario: Exact path

- **WHEN** an override declares the path `/api/payments/bilulu` and a request for that path arrives
- **THEN** the override matches

#### Scenario: Path with a segment parameter

- **WHEN** an override declares the path `/zip/:id/json` and requests arrive for `/zip/40415345/json` and for `/zip/40415345/extra/json`
- **THEN** the override matches the first and does not match the second

#### Scenario: Path with a wildcard

- **WHEN** an override declares the path `/api/payments/*` and a request for `/api/payments/charge/42` arrives
- **THEN** the override matches

#### Scenario: Restriction by method

- **WHEN** an override declares the path `/api/payments/charge` restricted to the `POST` method and a `GET` for that path arrives
- **THEN** the override does not match and the request is forwarded to the upstream

#### Scenario: JSON equality on the body ignores key order

- **WHEN** an override requires the body to equal the JSON `{"a":1,"b":2}` and a request arrives with the body `{"b":2,"a":1}`
- **THEN** the override matches

#### Scenario: One criterion that does not match prevents interception

- **WHEN** an override requires the header `X-Tenant: acme` and a request arrives without that header
- **THEN** the override does not match

### Requirement: Precedence by specificity

When more than one override matches the same request, the gateway SHALL apply the most specific one. An exact path MUST prevail over a path with segment parameters, which MUST prevail over a wildcard (among paths with parameters, the one with more literal segments prevails), a longer wildcard MUST prevail over a shorter one, and among paths of equal specificity the override with more declared criteria MUST prevail. Remaining ties MUST be resolved by declaration order in the route document.

#### Scenario: An exact path beats a wildcard

- **WHEN** overrides exist for `/api/payments/*` and for `/api/payments/bilulu`, and a request for `/api/payments/bilulu` arrives
- **THEN** the exact-path override is applied

#### Scenario: A segment parameter sits between exact and wildcard

- **WHEN** overrides exist for `/zip/*`, `/zip/:id/json` and `/zip/01001000/json`, and requests arrive for `/zip/01001000/json` and `/zip/40415345/json`
- **THEN** the first is served by the exact override and the second by the segment parameter one

#### Scenario: A longer wildcard beats a shorter one

- **WHEN** overrides exist for `/api/*` and `/api/payments/*` and a request for `/api/payments/charge` arrives
- **THEN** the `/api/payments/*` override is applied

#### Scenario: More criteria wins among equivalent paths

- **WHEN** two overrides declare the same path and one of them also requires the `POST` method, and a `POST` for that path arrives
- **THEN** the override that requires the method is applied

### Requirement: Response declared by the override

An override that intercepts SHALL respond with the status, headers and body it declares. When the status is not given, the gateway MUST use `200`. The body MAY be declared as text or as a JSON structure, and in the latter case the gateway MUST serialize it and set the corresponding content type, unless the override declares another.

#### Scenario: Full response returned

- **WHEN** an override declares status `200`, the header `X-Source: override` and a JSON body, and intercepts a request
- **THEN** the client receives exactly that status, that header and that body, and the upstream is not contacted

#### Scenario: Default status

- **WHEN** an override declares only a body and intercepts a request
- **THEN** the client receives status `200`

#### Scenario: Content type inferred from a JSON body

- **WHEN** an override declares the body as a JSON structure and declares no content type
- **THEN** the response is serialized as JSON and carries the corresponding content type

### Requirement: Frequency of each effect

Each effect an override declares — the declared response, the latency and the connection drop — MAY declare its own frequency, between `0.0` and `1.0`, and the gateway SHALL draw each effect independently on every request the override selects. An effect with no declared frequency MUST always apply. An effect that is not drawn MUST be ignored as if it were not declared, and a request where no effect was drawn MUST go to the upstream unchanged. Values outside the range MUST be refused during validation. For compatibility, a frequency declared for the whole override MUST act as the default for every one of its effects that does not declare its own.

#### Scenario: An effect with no frequency always applies

- **WHEN** an override declares a response with no frequency and intercepts 20 requests
- **THEN** all 20 are answered by it

#### Scenario: Each effect has its own frequency

- **WHEN** an override declares a `503` response with frequency `0.3` and a latency of `1s` with no frequency, with a fixed seed, and 1000 requests it selects arrive
- **THEN** the count answered with `503` falls within the statistical tolerance expected for 30%, the rest are forwarded to the upstream, and all 1000 are delayed

#### Scenario: A drop with its own frequency

- **WHEN** an override declares a drop with frequency `0.05` and a declared response with no frequency, and 1000 requests it selects arrive
- **THEN** about 5% of the requests are dropped and the rest receive the declared response

#### Scenario: Frequency zero never applies

- **WHEN** an effect declares frequency `0.0` and requests the override selects arrive
- **THEN** that effect is never applied, and the override's other effects keep applying

#### Scenario: A frequency outside the range is refused

- **WHEN** a route document declares frequency `1.5` on an effect
- **THEN** the configuration is refused with a message pointing at the invalid field

#### Scenario: The override's frequency applies to the effects without one

- **WHEN** an override declares frequency `0.3` for itself, a response with no frequency of its own and a latency with frequency `1.0`
- **THEN** the response is drawn on 30% of the requests and the latency is applied on all of them

### Requirement: Latency and connection drop

An override MAY declare latency, either as a fixed value or as a range with a minimum and a maximum drawn uniformly on every request, and MAY declare a connection drop. The latency MUST be applied once the response is ready and before delivering it to the client. The drop MUST end the connection without sending any response and MUST take precedence over the declared response.

#### Scenario: Fixed latency

- **WHEN** an override declares a latency of `2s` and intercepts a request
- **THEN** the response reaches the client at least `2s` after the request

#### Scenario: Latency drawn from the range

- **WHEN** an override declares latency with a minimum of `100ms` and a maximum of `500ms` and intercepts 50 requests
- **THEN** every observed delay falls between `100ms` and `500ms` and they are not all the same

#### Scenario: An inverted range is refused

- **WHEN** a route document declares latency with a minimum of `500ms` and a maximum of `100ms`
- **THEN** the configuration is refused with a message pointing at the invalid field

#### Scenario: A drop ends with no response

- **WHEN** an override declares a connection drop and intercepts a request
- **THEN** the client observes the connection closed without receiving a status or a body

#### Scenario: Latency applied to a route with no interception

- **WHEN** an override declares only latency, with no declared response, and selects a request
- **THEN** the request is forwarded to the upstream normally and its response is delivered after the delay

### Requirement: Expiry by time and by count

An override MAY declare a time to live and MAY declare a maximum number of applications. The gateway SHALL stop applying it when either one runs out, with no user intervention, and MUST expose the remaining time and the application count while it is active. An override with neither MUST stay active until it is removed.

#### Scenario: An override expires by time

- **WHEN** an override with a time to live of `30s` is registered and more than `30s` pass
- **THEN** the following requests go back to being forwarded to the upstream, with no action from the user

#### Scenario: An override expires by count

- **WHEN** an override declares a maximum of two applications and three requests it selects arrive
- **THEN** the first two are answered by the override and the third is forwarded to the upstream

#### Scenario: Remaining time and count are queryable

- **WHEN** an override with a time to live of `60s` and a limit of five applications has been active for `20s` and has already applied twice
- **THEN** the query reports roughly `40s` remaining and two applications out of a limit of five

#### Scenario: An override with no limits stays

- **WHEN** an override with no time to live and no count limit is registered and a long period passes
- **THEN** it keeps being applied until it is explicitly removed

### Requirement: Determinism by seed

The gateway SHALL accept a seed for its probabilistic decisions. With the same seed and the same sequence of requests, the gateway MUST make exactly the same application decisions, effect by effect, in the same order. With no seed declared, the gateway MUST use a non-reproducible source of randomness.

#### Scenario: The same seed reproduces the same sequence

- **WHEN** the gateway is run twice with the same seed and receives the same sequence of 100 requests in each run
- **THEN** the set of intercepted requests is identical in both runs

#### Scenario: Different seeds diverge

- **WHEN** the gateway is run with two different seeds over the same sequence of requests
- **THEN** the set of intercepted requests differs between the runs

### Requirement: Intervention identification

Every response synthesized or delayed by an override SHALL identify, in the `X-Gateway` header defined by the `gateway-routing` spec, the override responsible and the kind of intervention applied. The corresponding exchange MUST be recorded as intercepted.

#### Scenario: An intercepted response is identifiable

- **WHEN** an override synthesizes a `503` response
- **THEN** the response's `X-Gateway` header states the override responsible and that the response was synthesized by the gateway

#### Scenario: An upstream response is not marked

- **WHEN** the upstream responds `500` on its own and no override intercepted the request
- **THEN** the response's `X-Gateway` header identifies only the route, with no override and no intervention

### Requirement: Enabled and disabled overrides

An override SHALL be declarable as disabled. A disabled override MUST NOT take part in selection or in precedence, and the requests it would select MUST proceed as if it did not exist. The absence of the field MUST mean enabled. Disabling and re-enabling an override MUST preserve all of its other fields.

#### Scenario: A disabled override does not intercept

- **WHEN** an override that synthesizes `503` is disabled and a request it selects arrives
- **THEN** the request is forwarded to the upstream

#### Scenario: A disabled override does not hide the less specific one

- **WHEN** an exact-path override is disabled and a wildcard override that also matches is enabled
- **THEN** the wildcard override is applied

#### Scenario: Re-enabling restores the behavior

- **WHEN** the disabled override is re-enabled with no other change
- **THEN** the following requests go back to being answered by it with the same declared response

### Requirement: Endpoint learning

The gateway SHALL offer a learning mode, off by default and changeable at runtime. With the mode off, the gateway MUST only apply the existing configuration, writing nothing beyond the history. With the mode on, every combination of method and path not yet known on a route, whose request was forwarded and answered by the upstream, MUST be written to that route's document as a disabled override, with a method criterion and a generalized path criterion — segments that identify a record (all digits, a UUID, or hexadecimal/alphanumeric with digits and at least 8 characters) replaced by the segment parameters `:id`, `:id2`… and the rest kept literal — and a declared response pre-filled with the status, all the headers and the body observed — excluding only `Date`, `Content-Length` and hop-by-hop headers. The override written MUST record the originating exchange and whether the body was truncated during capture. A combination is known when the route already has an override, enabled or disabled, of the same method with an exact path or with segment parameters that matches the request; wildcard or regular expression overrides do not make a combination known. When writing a generalized override, previously learned overrides with an exact path of the same method whose path generalizes to its own MUST be replaced by it, without duplicating the rule, provided they remain as they were learned (disabled and with no additional criteria); a learned override the user enabled or restricted is preserved.

#### Scenario: New endpoints are learned

- **WHEN** learning mode is on and `GET /api/test` and then `GET /api/test2` arrive through a route with an upstream
- **THEN** the route document now holds two disabled overrides, one for each path, each with the real response the upstream returned

#### Scenario: A learned endpoint does not intercept

- **WHEN** an endpoint has been learned and the same request arrives again
- **THEN** it is forwarded to the upstream normally, because the learned override is disabled

#### Scenario: A known endpoint is not duplicated

- **WHEN** learning mode is on and `GET /api/test` arrives for the second time
- **THEN** the route document still holds a single override for `GET /api/test`

#### Scenario: With the mode off nothing is written

- **WHEN** learning mode is off and requests for new paths arrive
- **THEN** no route document is modified and the exchanges appear only in the history

#### Scenario: Only upstream responses are learned

- **WHEN** learning mode is on and the request is answered by an override, by a `404` with no route or by a `502` from the gateway
- **THEN** no override is learned from it

#### Scenario: An identifier becomes a parameter

- **WHEN** learning mode is on and `GET /zip/40415345/json` and then `GET /zip/01001000/json` arrive
- **THEN** the route document now holds a single disabled override with the path `/zip/:id/json`

#### Scenario: A literal segment is preserved

- **WHEN** learning mode is on and `GET /api/users/me` and `GET /api/users/42` arrive
- **THEN** `/api/users/me` and `/api/users/:id` are learned as distinct overrides

#### Scenario: An exact learned override is absorbed

- **WHEN** the route already has a learned override with the path `/zip/40415345/json` and learning writes `/zip/:id/json` for the same method
- **THEN** the exact learned override is replaced by the generalized one and the document ends up with a single one

#### Scenario: A truncated body is flagged

- **WHEN** learning mode is on and the observed response exceeds the capture limit
- **THEN** the learned override records that the body is incomplete

### Requirement: Deriving an override from a captured exchange

The gateway SHALL allow creating an override from an exchange already recorded in the history, filling the criteria with the data of the observed request and the declared response with what the upstream returned. The derived override MUST be reviewable before it takes effect.

#### Scenario: A derived override reproduces the observed exchange

- **WHEN** an override is derived from a captured exchange and an equivalent request arrives afterwards
- **THEN** the gateway responds with the same status, headers and body the upstream had returned in that exchange

#### Scenario: Nonexistent exchange

- **WHEN** derivation is requested from an exchange identifier that does not exist in the history
- **THEN** the operation is refused with an error reporting that the exchange was not found

#### Scenario: Derivation from a truncated exchange

- **WHEN** derivation is requested from an exchange whose body was truncated during capture
- **THEN** the operation is refused, or the override is created while explicitly flagging that the body is incomplete
