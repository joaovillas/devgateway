## Purpose

Keeps the process configuration in `gateway.json` and each route in its own document under `routes/`, versionable and with English keys, so that the team's environment lives in the repository, can be reviewed route by route, and produces no conflict when two people create different services.

## ADDED Requirements

### Requirement: Process configuration in gateway.json

The gateway SHALL load the process configuration from `gateway.json`: the traffic and admin ports, the seed, the history storage backend and its parameters, history exposure and recording, capture limits, learning mode and the routes directory. Every key MUST be in English. When the file does not exist, the gateway MUST start with the default values and log a warning identifying the path it looked for.

#### Scenario: Process configuration loaded

- **WHEN** the gateway starts with a `gateway.json` declaring both ports and the seed
- **THEN** the process serves on those ports and uses that seed for its probabilistic decisions

#### Scenario: Process file missing

- **WHEN** the gateway starts and `gateway.json` does not exist
- **THEN** the gateway comes up with the default values, logs a warning identifying the path and stays operational

### Requirement: One document per route

The gateway SHALL load each route from its own YAML document inside the routes directory, with English keys, and MUST merge every document it finds into a single snapshot. Each document MUST declare the complete route — upstream, matching and overrides. An invalid document MUST prevent the load, identifying the file responsible, rather than being silently ignored.

#### Scenario: Documents merged into a snapshot

- **WHEN** the routes directory contains three valid documents
- **THEN** all three routes serve traffic after startup

#### Scenario: Routes directory empty or missing

- **WHEN** the routes directory is empty or does not exist
- **THEN** the gateway comes up with no routes, logs a warning and accepts routes through the admin API

#### Scenario: An invalid document identifies the file

- **WHEN** one of the documents in the directory declares an invalid field
- **THEN** the load is refused with a message naming the file, the field and its location inside it

#### Scenario: A file without a recognized extension is ignored

- **WHEN** the routes directory contains a file that is not a YAML document
- **THEN** that file is ignored without preventing the load of the others

### Requirement: Collision detection between documents

The gateway SHALL refuse the load when two route documents declare the same route name, or when they declare the same host and the same path pattern. The message MUST name both conflicting files.

#### Scenario: Duplicate route names

- **WHEN** two documents declare routes with the same name
- **THEN** the load is refused, reporting both files and the duplicated name

#### Scenario: Identical matching patterns

- **WHEN** two documents declare the same host and the same path pattern
- **THEN** the load is refused, reporting both files and the conflicting pattern

#### Scenario: Distinct overlapping patterns are allowed

- **WHEN** one document declares `/api/*` and another declares `/api/payments/*`
- **THEN** both are loaded and precedence by specificity resolves the match

### Requirement: Configuration validation

The gateway SHALL validate the whole configuration before applying it and MUST refuse invalid configuration with a message identifying the file, the offending field and its location. The gateway MUST NOT start with invalid configuration.

#### Scenario: Probability outside the range

- **WHEN** a route document declares probability `1.5` on an override
- **THEN** the load is refused, naming the file, the field and its location

#### Scenario: Invalid upstream

- **WHEN** a document declares an upstream whose address is not a valid URL
- **THEN** the load is refused, reporting the file and the rejected value

#### Scenario: Identical ports

- **WHEN** `gateway.json` declares the same port for traffic and administration
- **THEN** the gateway refuses to start, reporting the conflict

#### Scenario: Schema version higher than the one known

- **WHEN** a document declares a schema version higher than the binary supports
- **THEN** the load is refused with a message naming both versions

### Requirement: Environment variables override the files

The gateway SHALL accept environment variables for the process configuration, and these MUST take precedence over `gateway.json`, which in turn MUST take precedence over the default values. The effective origin of each value MUST be queryable, so that the user knows whether a value came from the environment, from the file or from the default.

#### Scenario: The environment beats the file

- **WHEN** `gateway.json` declares the history backend as memory and the corresponding environment variable declares SQLite
- **THEN** the gateway uses SQLite

#### Scenario: The file beats the default

- **WHEN** no environment variable is set and `gateway.json` declares a traffic port different from the default
- **THEN** the gateway serves on the port declared in the file

#### Scenario: Effective origin is queryable

- **WHEN** the effective configuration is queried with the port coming from the environment and the seed coming from the file
- **THEN** the query reports, for each value, whether it came from the environment, from the file or from the default

### Requirement: Reload without restart

The gateway SHALL apply any configuration change — a reload of the files or a change through the API — without terminating the process or dropping in-flight connections, including changes to ports and to the history backend. When the new configuration is invalid or cannot be applied, the gateway MUST keep the previous configuration in force and report the error.

#### Scenario: A reload applies the new configuration

- **WHEN** a new route document is added to the directory and a reload is requested
- **THEN** the new route starts serving without the process being restarted

#### Scenario: An invalid reload preserves the previous configuration

- **WHEN** a document is edited with an invalid value and a reload is requested
- **THEN** the reload is refused with the validation message and the previous routes keep serving

#### Scenario: In-flight requests survive the reload

- **WHEN** there are requests in flight and a valid reload is applied
- **THEN** the in-flight requests complete under the configuration they started with

#### Scenario: Port hot-swapped

- **WHEN** the traffic port is changed to a free port while requests are in flight on the current one
- **THEN** the gateway starts serving on the new port, stops accepting connections on the old one and completes the in-flight requests, without restarting the process

#### Scenario: An unavailable new port preserves the current one

- **WHEN** the traffic port is changed to a port already in use
- **THEN** the change is refused, reporting the port and the cause, and the gateway keeps serving on the current port

#### Scenario: History backend hot-swapped

- **WHEN** the history backend is changed from memory to SQLite with the gateway running
- **THEN** SQLite is initialized before the swap, the following exchanges are recorded in it and the previous history is not migrated

#### Scenario: An unavailable new backend preserves the current one

- **WHEN** the history backend is changed to one that cannot be initialized
- **THEN** the change is refused, reporting the backend and the cause, and the current backend stays in use

### Requirement: Admin API

The gateway SHALL expose an admin API that allows querying and changing everything `gateway.json` and the route documents configure — routes, overrides and the process configuration — and querying the traffic history. A route change MUST rewrite only that route's document, leaving the others untouched, and a process change MUST be written to `gateway.json`. A value set by an environment variable MUST NOT be changeable through the API, and the refusal MUST name the variable responsible. Concurrent writes MUST be serialized so that none is lost, and each document's write MUST be atomic.

#### Scenario: A change touches only the route's document

- **WHEN** an override is added to the `payments` route through the API and the directory's documents are read afterwards
- **THEN** only `payments` was rewritten and the other documents stay byte for byte the same

#### Scenario: Creating a route produces its own document

- **WHEN** a new route is created through the API
- **THEN** a corresponding document appears in the routes directory

#### Scenario: Concurrent writes are serialized

- **WHEN** two changes to different routes are submitted simultaneously
- **THEN** both are applied and both documents reflect the changes

#### Scenario: Process configuration changed through the API

- **WHEN** the seed is changed through the API
- **THEN** the new seed takes effect without a restart and `gateway.json` starts declaring it

#### Scenario: A value from the environment is locked

- **WHEN** the traffic port comes from an environment variable and the API receives a change to that port
- **THEN** the change is refused, naming the environment variable responsible, and `gateway.json` is not modified

#### Scenario: An invalid change is refused without touching the disk

- **WHEN** the API receives an override whose minimum latency is greater than its maximum
- **THEN** the change is refused with the validation message and no document is modified

### Requirement: Separation between the traffic port and the admin port

The gateway SHALL serve forwarded traffic and administration on separate ports. The admin API and the interface MUST NOT be reachable on the traffic port, and no reserved path MUST be taken out of the user's route space.

#### Scenario: Administration unavailable on the traffic port

- **WHEN** a request for an admin API path arrives on the traffic port
- **THEN** it is treated as ordinary traffic, subject to the route resolution the user configured

#### Scenario: Traffic unavailable on the admin port

- **WHEN** a request for a configured route's path arrives on the admin port
- **THEN** it is not forwarded to any upstream
