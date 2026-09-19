## Purpose

Receives all development traffic on a single port and forwards every request to the right upstream service, so that clients stop having to know each service's individual port.

## ADDED Requirements

### Requirement: Path wildcard routing

The gateway SHALL forward each request to the upstream of the route whose path pattern matches it. The pattern MUST accept the prefix form with a suffix wildcard, such as `/api/payments/*`, and the exact path form. When more than one route matches, the gateway MUST pick the most specific pattern — an exact path before a wildcard, and a longer wildcard before a shorter one. Each route MAY declare that the prefix is stripped before forwarding.

#### Scenario: The most specific wildcard wins

- **WHEN** the routes `/api/*` and `/api/payments/*` both exist and a request for `/api/payments/123` arrives
- **THEN** the request is forwarded to the upstream of the `/api/payments/*` route

#### Scenario: Prefix stripped before forwarding

- **WHEN** the `/api/payments/*` route is configured to strip the prefix and a request for `/api/payments/123` arrives
- **THEN** the upstream receives the request on the path `/123`

#### Scenario: Prefix preserved by default

- **WHEN** the route does not declare prefix stripping and a request for `/api/payments/123` arrives
- **THEN** the upstream receives the request on the path `/api/payments/123`

#### Scenario: No route matches

- **WHEN** a request arrives for a path no route covers
- **THEN** the gateway responds `404` with a body stating that no route matched and listing the configured patterns

### Requirement: Host routing

The gateway SHALL allow a route to require a specific host, matching against the request's `Host` header. A route MAY combine host and path pattern, and in that case both criteria MUST match.

#### Scenario: Routing by host alone

- **WHEN** a route exists for the host `payments.local` and a request arrives with `Host: payments.local`
- **THEN** the request is forwarded to that route's upstream

#### Scenario: Host and path combined

- **WHEN** a route exists for the host `payments.local` with the pattern `/v2/*` and a request arrives with `Host: payments.local` for the path `/v1/charge`
- **THEN** that route does not match and the request continues through the resolution of the remaining routes

#### Scenario: A route with a host takes precedence over one without

- **WHEN** a route with a host and a path-only route both match the same request
- **THEN** the route with the host is chosen

### Requirement: Header forwarding

The gateway SHALL pass through every incoming header, including the original `Host`, and MUST NOT remove or alter any of them, except for the hop-by-hop headers the HTTP protocol forbids forwarding. The gateway SHALL add `X-Forwarded-For`, `X-Forwarded-Proto` and `X-Forwarded-Host`, preserving values that already arrive filled in. A route MAY choose to replace the `Host` with the upstream's host.

#### Scenario: Forwarding headers added

- **WHEN** a request with no forwarding headers is forwarded to an upstream
- **THEN** the upstream receives `X-Forwarded-For` with the client's address, `X-Forwarded-Proto` with the original scheme and `X-Forwarded-Host` with the original host

#### Scenario: Original host passed through by default

- **WHEN** the route does not declare host replacement and a request arrives with `Host: payments.local`
- **THEN** the upstream receives `Host: payments.local`

#### Scenario: Host replaced on demand

- **WHEN** the route declares host replacement
- **THEN** the upstream receives the host of its own address in the `Host` header

#### Scenario: X-Forwarded-For accumulates the chain

- **WHEN** the request already arrives with `X-Forwarded-For` filled in
- **THEN** the gateway appends the client's address to the existing value instead of replacing it

#### Scenario: X-Forwarded-Host and X-Forwarded-Proto preserved

- **WHEN** the request already arrives with `X-Forwarded-Host` and `X-Forwarded-Proto` filled in
- **THEN** the upstream receives those values as they arrived

#### Scenario: Arbitrary headers passed through

- **WHEN** the request arrives with custom, repeated and authorization headers
- **THEN** the upstream receives all of them, with the same values and in the same quantity

### Requirement: Gateway identification header

The gateway SHALL add a single header of its own, `X-Gateway`, to the request forwarded to the upstream and to the response delivered to the client, identifying the matched route. When an override intervenes, the same header on the response MUST also identify the override and the kind of intervention. The gateway MUST NOT add any header other than this one and the forwarding headers.

#### Scenario: Identification without an intervention

- **WHEN** a request is forwarded by the `payments` route with no intervention
- **THEN** the upstream and the client both receive `X-Gateway` identifying the `payments` route, with no override and no intervention

#### Scenario: Identification with an intervention

- **WHEN** the `flaky` override of the `payments` route synthesizes the response
- **THEN** the client receives `X-Gateway` identifying the route, the override `payments/flaky` and the intervention `synthesized`

### Requirement: Upstream failure handling

The gateway SHALL respond `502` when it cannot establish a connection to the upstream and `504` when the upstream exceeds the timeout configured for the route. The response body MUST identify the route and the upstream involved.

#### Scenario: The upstream refuses the connection

- **WHEN** a route's upstream is not accepting connections and a request for it arrives
- **THEN** the gateway responds `502` with a body naming the route and the upstream's address

#### Scenario: The upstream exceeds the timeout

- **WHEN** the upstream takes longer than the configured timeout to respond
- **THEN** the gateway responds `504` and ends the request to the upstream

#### Scenario: An upstream failure does not bring the gateway down

- **WHEN** an upstream fails repeatedly
- **THEN** the remaining routes keep being served normally

### Requirement: Transparency of forwarded traffic

The gateway SHALL forward any HTTP method, preserving the path, query, body and incoming headers, and, on the way back, the response's status, headers and body unaltered, except for the headers the gateway itself adds, the prefix stripping declared by the route and the interventions declared by an override. Streaming responses MUST be passed through incrementally, without waiting for the complete body.

#### Scenario: Method and body preserved

- **WHEN** a `POST` arrives with a binary body and a specific `Content-Type` header
- **THEN** the upstream receives the same method, the same body byte for byte and the same `Content-Type`

#### Scenario: Upstream status passed through

- **WHEN** the upstream responds `418`
- **THEN** the client receives `418` with the same headers and body

#### Scenario: Streaming response passed through incrementally

- **WHEN** the upstream responds with `text/event-stream` and emits events over time
- **THEN** the client receives each event as it is emitted, without waiting for the response to end
