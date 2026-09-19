## Purpose

Records every HTTP exchange that crosses the gateway with its time broken down and stores it in the backend the environment selects, so the developer can see the path the call took, tell the upstream's real slowness apart from the slowness the gateway injected, and read the exchanges one by one.

## ADDED Requirements

### Requirement: Recording HTTP exchanges

The gateway SHALL record every exchange that crosses the traffic port containing, at a minimum: a unique identifier, the start instant, the request's method, path, query, headers and body, the matched route, the override applied when there is one, the destination upstream, the response's status, headers and body, and the sizes in bytes. The identifier MUST be stable and sufficient to retrieve the exchange on its own. Bodies larger than the configured limit MUST be truncated and marked as truncated.

#### Scenario: A forwarded exchange is recorded in full

- **WHEN** a request is forwarded to an upstream and answered
- **THEN** the history now holds an exchange with its own identifier, the matched route, the destination upstream, the status and the request and response data

#### Scenario: A body above the limit is truncated

- **WHEN** a response carries a body larger than the configured capture limit
- **THEN** the exchange records the truncated body, flags that truncation happened and preserves the real size in bytes

#### Scenario: An exchange with no matched route is recorded too

- **WHEN** a request arrives that no route serves and the gateway responds `404`
- **THEN** the history records the exchange with no matched route and no upstream

### Requirement: Latency breakdown

Every recorded exchange SHALL report separately the total time, the time consumed by the upstream, the delay time injected by an override and the remaining time attributed to the gateway itself. When no delay is injected, the injected time MUST be zero.

#### Scenario: Injected time separated from real time

- **WHEN** an override with a latency of `2s` lets the request go to an upstream that responds in `150ms`
- **THEN** the exchange records roughly `2s` of injected time and roughly `150ms` of upstream time, and the total reflects the sum plus the gateway's overhead

#### Scenario: With no override the injected time is zero

- **WHEN** a request is forwarded without any override intercepting it
- **THEN** the exchange records an injected time of zero

#### Scenario: A synthesized response records no upstream time

- **WHEN** a request is answered by an override, with no contact with an upstream
- **THEN** the exchange records an upstream time of zero

### Requirement: Distinction between an upstream response and a gateway intervention

Each exchange's record SHALL state whether the result came from the upstream or was produced by an override, naming the override responsible when there is one. Connection drops MUST be recorded, even though no response was sent.

#### Scenario: A synthesized response is marked

- **WHEN** an override synthesizes a `503`
- **THEN** the exchange records status `503`, marks it as synthesized by the gateway and names the override responsible

#### Scenario: An upstream error is not marked as an intervention

- **WHEN** the upstream responds `500` without any override having intercepted
- **THEN** the exchange records status `500` with no intervention marking

#### Scenario: A connection drop is recorded

- **WHEN** an override drops the connection without sending a response
- **THEN** the exchange is recorded as ended by a drop, with no response status

### Requirement: Querying and filtering the history

The gateway SHALL allow querying the history in reverse chronological order, with pagination, and filtering it by route, upstream, override, method, path, status range, presence of an intervention and time window. Combined filters MUST be applied conjunctively.

#### Scenario: Filter by route

- **WHEN** the history is queried filtering by the `payments` route
- **THEN** only exchanges matched by that route are returned

#### Scenario: Filter by status range

- **WHEN** the history is queried filtering by status between `500` and `599`
- **THEN** only exchanges with a status in that range are returned

#### Scenario: Filter by intervention

- **WHEN** the history is queried filtering by exchanges with a gateway intervention
- **THEN** only exchanges synthesized or delayed by an override are returned

#### Scenario: Order and pagination

- **WHEN** the history holds 150 exchanges and the first page of 50 is requested
- **THEN** the 50 most recent exchanges are returned, newest to oldest, with a continuation indicator

### Requirement: Single reads and cursor navigation

The gateway SHALL allow retrieving a single exchange by its identifier, with the complete content even though the listing summarizes it. The gateway SHALL also allow navigating the history item by item from an exchange, obtaining the previous and the next one without loading the whole listing. The navigation MUST honor the active filters when they are given.

#### Scenario: An exchange retrieved by identifier

- **WHEN** an exchange is requested by its identifier
- **THEN** the gateway returns the complete exchange, with its request, response and time breakdown

#### Scenario: Nonexistent identifier

- **WHEN** an exchange is requested whose identifier does not exist in the history
- **THEN** the gateway responds that the exchange was not found

#### Scenario: Item-by-item navigation

- **WHEN** the next exchange is requested from an identifier
- **THEN** the gateway returns the exchange immediately after it in the history's order, or reports that there are no more items

#### Scenario: Navigation honors the active filter

- **WHEN** item-by-item navigation is done with a status range filter
- **THEN** the exchanges traversed are only those that satisfy the filter

### Requirement: Pluggable history storage

The gateway SHALL store the history in the backend selected by the configuration — an environment variable, `gateway.json` or the admin API — among memory, an NDJSON file and a local SQLite database. With no explicit selection, the gateway MUST use memory. The observable behavior of recording, querying and navigation MUST be the same across every backend. When the selected backend cannot be initialized, the gateway MUST refuse to start with a message identifying the backend and the cause, instead of silently falling back to another.

#### Scenario: Memory is the default

- **WHEN** the gateway starts with no configuration selecting the backend
- **THEN** the history is kept in memory, with a bounded capacity and the oldest exchanges discarded once it is reached

#### Scenario: The history survives a restart on a persistent backend

- **WHEN** the NDJSON or SQLite backend is selected, exchanges are recorded and the process is restarted
- **THEN** the previous exchanges remain available for querying and navigation

#### Scenario: The same behavior across backends

- **WHEN** the same sequence of requests crosses the gateway on each of the backends
- **THEN** listing, filters, reads by identifier and cursor navigation produce the same results

#### Scenario: An unavailable backend prevents startup

- **WHEN** the SQLite backend is selected with a path the gateway cannot write to
- **THEN** the gateway refuses to start, reporting the backend and the cause of the failure

#### Scenario: Capacity exceeded in memory

- **WHEN** the backend is memory, the capacity is 100 exchanges and the hundred and first arrives
- **THEN** the oldest exchange drops out of the history and the new one is recorded

### Requirement: Configurable history exposure

The gateway SHALL allow turning history exposure on and off through configuration. With exposure off, the listing, read and navigation endpoints MUST respond that the feature is disabled, and the interface MUST say so instead of presenting an empty list. Recording exchanges MUST be turnable off independently of exposure.

#### Scenario: Exposure off

- **WHEN** history exposure is off and the listing is requested
- **THEN** the gateway responds that the feature is disabled, returning no exchanges

#### Scenario: Recording off

- **WHEN** recording is off and requests arrive
- **THEN** no exchange is recorded and forwarding keeps working normally

#### Scenario: Clearing on demand

- **WHEN** clearing the history is requested
- **THEN** the history is empty on the backend in use and the following exchanges go back to being recorded normally
