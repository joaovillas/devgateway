## Purpose

Gives the developer a navigable view of the path a call takes and direct controls over the overrides, so that configuring "this route fails 30% of the time" is a gesture on screen and not an edit to a configuration document.

## ADDED Requirements

### Requirement: Interface served by the binary itself

The gateway SHALL serve the web interface from the executable itself, on the admin port, without requiring an additional process, an external server or access to the public network.

#### Scenario: Interface available with no dependencies

- **WHEN** the binary is run in an environment with no internet access and the admin port is opened in a browser
- **THEN** the interface loads completely, with all of its static assets

#### Scenario: The interface reflects the current state

- **WHEN** the interface is opened with routes and overrides already configured
- **THEN** it displays those routes and overrides without requiring any synchronization step

### Requirement: Services in map and list

The interface SHALL present the configuration in two views of the same set of services, where each service corresponds to a route and shows its name, the entry (the host and path matching through which the app calls the gateway) and the destination (the upstream the gateway redirects to). The default view MUST be the map, with your app, the services and the destinations in connected columns; the dense list MUST be available as an alternative view, and the choice between them MUST be remembered across sessions. Search by name, entry or destination and the action to register a new service MUST work in both views and be visible at all times, whatever the selection. Both views MUST stay readable and operable with at least 50 services. Services with an active rule MUST be visually distinguishable from the rest, and selecting a service or a destination MUST filter the traffic inspection to it.

#### Scenario: Topology displayed

- **WHEN** three services point at two destinations and the panel is opened for the first time
- **THEN** the map shows your app connected to the three services, each service with its entry and connected to its destination, and the two destinations, one per group of services

#### Scenario: The list as an alternative view

- **WHEN** the list view is chosen and the panel is reloaded
- **THEN** the list shows the same services, each with its entry and its destination, and remains the view on display

#### Scenario: Many services

- **WHEN** there are 50 services
- **THEN** the map and the list display them with no overlap and no loss of readability, with the services column scrolling inside the panel and your app and the destinations connected to the services in view

#### Scenario: Search in both views

- **WHEN** there are 50 services and the name of one of them is typed into the search
- **THEN** the view on display narrows to the services matching the text and, on the map, to their destinations only

#### Scenario: A new service is always reachable

- **WHEN** a service is selected, on the map or in the list
- **THEN** the action to register a new service stays visible and opens the form without requiring the selection to be undone

#### Scenario: A service with an active rule is highlighted

- **WHEN** a service has an active rule
- **THEN** it is displayed with a visual highlight that sets it apart from the rest, stating the kind of intervention and the probability applied

#### Scenario: The selection filters the traffic

- **WHEN** a service is selected on the map or in the list
- **THEN** the traffic inspection narrows to that service's exchanges, and the map recedes whatever is not part of the selected path

#### Scenario: An unavailable destination is flagged

- **WHEN** a destination has been refusing connections on recent requests
- **THEN** the map flags the destination and the connections to it as unavailable, and the list flags the destination of the affected services

### Requirement: The user's vocabulary

The interface SHALL name the concepts by what the developer recognizes, not by the gateway's internal terms: "your app" for the caller, "service" for each registered route, "entry" for the host and path matching, "destination" for the upstream and "rule" for each override. The documents on disk and the admin API MAY keep the internal terms (route, upstream, override).

#### Scenario: Internal terms off the screen

- **WHEN** the interface is opened with services and rules configured
- **THEN** no visible label, title or button uses "route", "upstream" or "override" as the name of those concepts


### Requirement: Direct override control on the route

The interface SHALL allow adjusting probability, latency and connection drops directly on the override, in the panel of the service selected on the map or in the list, through a continuous control, applying the change immediately, with no separate confirmation or save step. When the override has a time to live or an application limit, the interface MUST display what is left of each.

#### Scenario: The adjustment is applied immediately

- **WHEN** an override's probability is adjusted to 30% on the continuous control
- **THEN** the following requests already follow the new proportion, with no additional save action

#### Scenario: Remaining time and applications displayed

- **WHEN** an override with a time to live of `60s` and a limit of five applications is active
- **THEN** the interface shows the remaining time counting down and the application count, and removes the route's highlight when either one runs out

#### Scenario: Turning the override off in one gesture

- **WHEN** an override is turned off through the interface
- **THEN** the following requests go back to being forwarded to the upstream and the route's highlight is removed

### Requirement: Simple mode and advanced mode

The interface SHALL offer two levels of detail, with simple mode as the default. In simple mode, each rule MUST show only the on/off switch, the name, the summarized effect (synthesized status, latency or drop) and the probability; advanced mode MUST reveal latency, drops, selection criteria, declared response, time to live and application limit, plus the complete fields of the service and of the process. The choice of mode MUST be remembered across visits. A rule that uses features revealed only in advanced mode MUST keep saying so in simple mode, so that nothing is hidden without warning.

#### Scenario: Simple is the default

- **WHEN** the interface is opened for the first time with a service selected
- **THEN** each rule shows only the on/off switch, the name, the effect and the probability

#### Scenario: Advanced reveals the rest

- **WHEN** advanced mode is turned on
- **THEN** latency, drops, criteria, response, time to live and application limit become editable on the same screen

#### Scenario: The mode is remembered

- **WHEN** advanced mode is turned on and the page is reloaded
- **THEN** the interface comes back in advanced mode

#### Scenario: An advanced feature is visible in simple mode

- **WHEN** a rule declares latency and an application limit and simple mode is active
- **THEN** the rule flags those settings in its summary, even without showing their controls

### Requirement: Trimmed traffic

The exchange list SHALL show by default only the time, method, path, status, duration and the temporal representation, with the intervention flagged next to the status. The service column MUST appear only when the list is not filtered by a service. The filters MUST stay collapsed behind a single control, and the active filters MUST stay visible and removable one by one.

#### Scenario: Trimmed columns

- **WHEN** the traffic inspection is opened filtered by a service
- **THEN** the list shows the time, method, path, status, duration and the waterfall, without the service column

#### Scenario: Filters collapsed

- **WHEN** the traffic inspection is opened with no filter
- **THEN** the filter controls take up no screen space and sit behind a single control

#### Scenario: An active filter stays visible

- **WHEN** a status range filter is applied and the filter control is closed
- **THEN** the active filter stays visible and can be removed in one gesture

### Requirement: Editing with parity to the documents

The interface SHALL allow querying and changing everything `gateway.json` and the route documents configure, exclusively through the admin API and only through visual controls. The interface MUST NOT display or require editing the documents' YAML or JSON content: they remain the source of truth on disk, editable in the user's editor, but the interface operates on the values. Values set by an environment variable MUST appear locked, stating the variable responsible.

#### Scenario: A control writes to the file

- **WHEN** an override's latency is changed on the visual control
- **THEN** the file on disk starts declaring the new latency, without the interface displaying the document

#### Scenario: An external edit is reflected

- **WHEN** a route document is edited outside the interface with a valid value and the configuration is reloaded
- **THEN** the visual controls reflect the new value without the page being reloaded

#### Scenario: An invalid value is refused

- **WHEN** a control receives a value that validation rejects
- **THEN** the interface shows the validation message next to the control and nothing is written

#### Scenario: A value from the environment is locked

- **WHEN** the history backend comes from an environment variable
- **THEN** the corresponding control appears locked and states the name of the variable

### Requirement: On/off switches for the override and for learning

The interface SHALL allow turning each override and learning mode on and off in a single gesture. Adjusting the probability, latency or drop of a disabled override MUST turn it on in the same gesture. Learned overrides MUST be distinguishable from declared ones and MUST lead to the originating exchange while it is still in the history.

#### Scenario: A learned override becomes chaos in one gesture

- **WHEN** a learned, disabled override has its probability adjusted to 30%
- **THEN** it becomes enabled at 30%, with no other action

#### Scenario: Learning mode toggled in the interface

- **WHEN** learning mode is turned on in the interface and a request for a new path arrives
- **THEN** the learned override appears on the route without the page being reloaded

#### Scenario: The originating exchange is reachable

- **WHEN** a learned override is opened
- **THEN** the interface offers navigation to the captured exchange it came from

### Requirement: Traffic inspection with a waterfall

The interface SHALL list the captured exchanges and, when one is selected, display the complete request and response alongside a temporal representation that visually separates the time consumed by the upstream from the time injected by the gateway. Exchanges with an intervention MUST be flagged in the list, and the interface MUST allow stepping through the exchanges one by one from the one that is open.

#### Scenario: The waterfall separates the times

- **WHEN** an exchange with `2s` of injected latency over an upstream that responded in `150ms` is selected
- **THEN** the temporal representation shows both periods as distinct, labeled segments

#### Scenario: An intervention is flagged in the list

- **WHEN** the list holds exchanges with upstream errors and exchanges synthesized by an override
- **THEN** the synthesized ones are flagged distinctly from the rest

#### Scenario: Item-by-item navigation

- **WHEN** an exchange is open and navigation to the next one is triggered
- **THEN** the interface opens the next exchange honoring the active filter, without going back to the listing

#### Scenario: Complete request and response

- **WHEN** an exchange is selected
- **THEN** the interface shows the method, path, headers and body of the request and of the response, flagging truncated bodies

### Requirement: Indication of a disabled history

When history exposure or recording is off, the interface SHALL state that condition explicitly instead of presenting an empty list, distinguishing "disabled" from "no exchanges yet".

#### Scenario: Exposure off

- **WHEN** history exposure is off and the traffic inspection is opened
- **THEN** the interface states that the history is disabled and indicates which setting controls it

#### Scenario: History enabled and empty

- **WHEN** the history is enabled and no exchange has happened yet
- **THEN** the interface states that there are no exchanges yet, without suggesting that the feature is disabled

### Requirement: Creating an override from a captured call

The interface SHALL allow creating an override from an exchange displayed in the inspection, presenting the criteria and the response already filled in with the observed data for review before they take effect.

#### Scenario: An override created from the exchange

- **WHEN** override creation is triggered on a captured exchange
- **THEN** the interface presents an override pre-filled with the data of that request and that response, awaiting confirmation

#### Scenario: The override is reviewed before it takes effect

- **WHEN** the pre-filled override is edited and confirmed
- **THEN** the following equivalent requests start being intercepted by the edited override

### Requirement: Real-time updates

The interface SHALL reflect new exchanges and configuration changes without the user having to reload the page. When the connection to the gateway is lost, the interface MUST signal the loss and MUST try to restore it automatically.

#### Scenario: A new exchange appears on its own

- **WHEN** a request crosses the gateway with the interface open on the traffic inspection
- **THEN** the exchange appears in the list without the page being reloaded

#### Scenario: Connection loss is signaled

- **WHEN** the gateway process becomes unreachable with the interface open
- **THEN** the interface signals that it is disconnected and goes back to updating on its own when the gateway returns
