---
name: gateway
description: Control panel for the development gateway, a dark console of fixed panels where color is always state.
colors:
  ground: "#0d1117"
  panel: "#151a22"
  panel-raised: "#1b212b"
  seam: "#262e3a"
  seam-strong: "#354050"
  grid-dot: "#2a3340"
  ink: "#d8dee7"
  ink-2: "#a3aebb"
  ink-3: "#808c9b"
  ink-bright: "#ffffff"
  override: "#5aa2ff"
  override-dim: "rgb(90 162 255 / 0.14)"
  injected: "#e5a53d"
  fault: "#f26b64"
  fault-dim: "rgb(242 107 100 / 0.14)"
  healthy: "#4cc26a"
  time-upstream: "#6d7f96"
  time-gateway: "#3e4a5a"
typography:
  panel-title:
    fontFamily: "Source Sans 3 Variable, Segoe UI, system-ui, sans-serif"
    fontSize: "0.9375rem"
    fontWeight: 600
    letterSpacing: "0.1em"
    fontFeature: "font-variant-caps: all-small-caps"
  label:
    fontFamily: "Source Sans 3 Variable, Segoe UI, system-ui, sans-serif"
    fontSize: "0.9375rem"
    fontWeight: 600
    letterSpacing: "0.08em"
    fontFeature: "font-variant-caps: all-small-caps"
  title:
    fontFamily: "Source Sans 3 Variable, Segoe UI, system-ui, sans-serif"
    fontSize: "0.9375rem"
    fontWeight: 600
  body:
    fontFamily: "Source Sans 3 Variable, Segoe UI, system-ui, sans-serif"
    fontSize: "0.8125rem"
    fontWeight: 400
    lineHeight: 1.4
    fontFeature: "tnum"
  data:
    fontFamily: "JetBrains Mono Variable, ui-monospace, Cascadia Mono, Consolas, monospace"
    fontSize: "0.75rem"
    fontWeight: 400
    fontFeature: "tnum, zero"
  document:
    fontFamily: "JetBrains Mono Variable, ui-monospace, Cascadia Mono, Consolas, monospace"
    fontSize: "0.75rem"
    fontWeight: 400
    lineHeight: 1.6
rounded:
  sm: "2px"
spacing:
  s-1: "2px"
  s-2: "4px"
  s-3: "8px"
  s-4: "12px"
  s-5: "16px"
  bar-h: "30px"
  panel-head-h: "30px"
  row-h: "26px"
components:
  button:
    backgroundColor: "{colors.panel-raised}"
    textColor: "{colors.ink}"
    rounded: "{rounded.sm}"
    padding: "0 12px"
    height: "24px"
  button-primary:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.ground}"
    rounded: "{rounded.sm}"
    padding: "0 12px"
    height: "24px"
  button-primary-hover:
    backgroundColor: "{colors.ink-bright}"
    textColor: "{colors.ground}"
  button-sm:
    backgroundColor: "{colors.panel-raised}"
    textColor: "{colors.ink}"
    rounded: "{rounded.sm}"
    padding: "0 8px"
    height: "22px"
  icon-button:
    textColor: "{colors.ink-3}"
    rounded: "{rounded.sm}"
    size: "22px"
  icon-button-hover:
    backgroundColor: "{colors.panel-raised}"
    textColor: "{colors.ink}"
  input:
    backgroundColor: "{colors.ground}"
    textColor: "{colors.ink}"
    rounded: "{rounded.sm}"
    padding: "0 8px"
    height: "24px"
  chip:
    textColor: "{colors.ink-2}"
    typography: "{typography.data}"
    rounded: "{rounded.sm}"
    padding: "0 8px"
    height: "20px"
  chip-override:
    backgroundColor: "{colors.override-dim}"
    textColor: "{colors.override}"
    rounded: "{rounded.sm}"
  panel:
    backgroundColor: "{colors.panel}"
    textColor: "{colors.ink}"
  panel-head:
    textColor: "{colors.ink-2}"
    typography: "{typography.panel-title}"
    padding: "0 12px"
    height: "30px"
  map-node:
    backgroundColor: "{colors.panel-raised}"
    textColor: "{colors.ink}"
    typography: "{typography.body}"
    rounded: "{rounded.sm}"
    padding: "0 8px"
    height: "34px"
  map-node-compact:
    height: "26px"
  map-node-selected:
    backgroundColor: "{colors.override-dim}"
    textColor: "{colors.ink}"
  map-field:
    backgroundColor: "{colors.panel}"
  service-row:
    textColor: "{colors.ink}"
    typography: "{typography.body}"
    padding: "0 12px"
    height: "26px"
  service-row-selected:
    backgroundColor: "{colors.override-dim}"
    textColor: "{colors.ink}"
  traffic-row:
    textColor: "{colors.ink}"
    typography: "{typography.body}"
    padding: "0 8px"
    height: "26px"
  traffic-row-open:
    backgroundColor: "{colors.override-dim}"
  rule-row-simple:
    padding: "8px 0"
    gap: "{spacing.s-2}"
  filters-bar:
    padding: "4px 12px 8px 8px"
    height: "24px"
---

# Design System: gateway

## Overview

**Creative North Star: "The Instrument Bench"**

The panel is a console of fixed panels on a graphite floor: services, traffic, detail and controls, each with its own place, separated by 1px seams and never stacked in layers. It is a bench the developer glances at on a second screen, so density is high, the ink is almost entirely neutral, and attention goes to the only signal that matters: what the gateway is doing to the traffic.

Color decorates nothing. Blue, amber, red and green are four states with fixed meaning, and everything else resolves into shades of graphite and ink. Numbers are always tabular, data is mono, and headings are discreet small caps. Depth comes from tone (ground, panel, raised panel), never from shadow. Motion is limited to one moment: the exchange that just arrived.

**Key Characteristics:**
- Fixed panels on a grid, separated by 1px seams in the `seam` color (a `gap: 1px` over a `seam` background).
- Color strictly semantic: blue for selection/override, amber for injected, red for drop/synthesized, green for healthy.
- Data in JetBrains Mono 12px with tabular figures and a slashed zero; the interface in Source Sans 3 at 13px.
- Near-square corners (2px) and low controls (22 to 26px tall).
- No elevation shadow; focus and selection states are 1px strokes.
- The dev's vocabulary on screen: your app → service → destination, with rules. The internal terms (route, upstream, override) stay in the API and in the files.
- Two levels of detail: simple (the default) shows the essentials of each rule; advanced reveals the full editing surface. The choice is kept in the browser.

## Colors

Cold graphite in three surface steps, ink in three contrast steps, and four state colors that appear only when there is something to say.

### Primary
- **Selection Blue** (`override`): everything selected, open or under an override: the service selected on the map or in the list, a service node with an active rule, the lit path on the map, a filtered destination, the open row in the traffic list, the register button with the form open, a switch turned on, an active filter, the focus ring and the cursor. Its translucent version (`override-dim`) is the background for selection, for selected text and for the arrival glow.

### Secondary
- **Injection Amber** (`injected`): time and delays injected by the gateway: the waterfall segment, the delay tag, the badge and the meter of the delay rule on the map and in the services list, and the track of the latency control.

### Tertiary
- **Drop Red** (`fault`): what the gateway dropped or synthesized, and a destination that is down: the tags, the rule badge and meter, the dot, the outline and the word "down" on the destination, the dashed connection to it on the map, and a synthesized status. `fault-dim` tints the entire top bar when the connection to the panel drops, and backs the status mark in the narrow layout.
- **Healthy Green** (`healthy`): only the state dot for a destination that is up and for a healthy connection. It never fills areas.

### Neutral
- **Graphite Ground** (`ground`): the background under everything, and the recessed interior of fields, code blocks and waterfall tracks.
- **Panel** (`panel`): the surface of each fixed panel, of the top bar and of the sticky headers.
- **Raised Panel** (`panel-raised`): buttons, row hover, state notices and drafts.
- **Seam** (`seam`): the 1px lines between panels, table rules, section dividers. **Strong Seam** (`seam-strong`): the outlines of controls and of map nodes, the map's connections, the rule meter's track, the scrollbar. **Grid Dot** (`grid-dot`): the map field's 1px dot grid, every 12px; texture, not state.
- **Ink** (`ink`), **Ink 2** (`ink-2`), **Ink 3** (`ink-3`): primary text, secondary (panel titles) and tertiary (labels, metadata, gutter). `ink-3` is the floor: it holds 4.5:1 over `panel`, and nothing readable goes below it.
- **Full Ink** (`ink-bright`): only the hover of elements already in ink (the primary button, the continuous control's thumb, the bar's link). It is currently literal in the CSS; when you touch it, promote it to a variable.
- **Destination Time** (`time-upstream`) and **Gateway Time** (`time-gateway`): the neutral segments of the waterfall, so that only the injected amber stands out.

### Named Rules
**The Rule of Color That Means.** Blue, amber, red and green appear only for the state they name. A button, a title or a read failure of the panel itself gets no state color: the primary button is full ink and the read error has its title in `ink`.

**The Rule of Traffic Red.** Red is what the gateway did to the traffic (a drop, a synthesized response) or a destination that is down. An error coming from the destination is ink with a dotted underline; an error from the gateway itself is `ink-2`. Neither of them steals the red.

**The Rule of Receding by Tone.** Outside the selection, whatever recedes loses its state color and drops to `panel`, `seam` and `ink-3`, without falling below readable contrast. Opacity is used only on the state dot of a receded service (0.6), on the map's receded connections (0.3) and on disabled controls (0.5).

## Typography

**Display Font:** none; the console has no display type.
**Body Font:** Source Sans 3 Variable (with Segoe UI, system-ui)
**Label/Mono Font:** JetBrains Mono Variable (with ui-monospace, Cascadia Mono, Consolas)

**Character:** a humanist, compact sans for the interface and an engineering mono for everything that is data. Both ship embedded in the binary; nothing is loaded from the network.

### Hierarchy
- **Panel title** (600, 15px in small caps, 0.1em tracking, `ink-2`): the name of each panel in the 30px header, the tabs and the section titles inside the service panel. Small caps shrink to the x-height, which is why the nominal size is 15px.
- **Label** (600 or 400, 15px in small caps, 0.08em tracking, `ink-3`): column headers in the traffic list, the map and the services list, labels in the top bar and in the filters, form groups. It always labels the control or data next to it.
- **Title** (600, 15px): the rule's name and, in mono, the request and the status in the exchange detail.
- **Body** (400, 13px, 1.4): all interface text. Running state and warning text stops at 62 to 64ch.
- **Data** (400, 12px mono, tabular with a slashed zero): methods, paths, statuses, durations, probabilities, ports, tags and numeric fields.
- **Message body** (400, 12px mono, 1.55): the request and response bodies in the exchange detail.
- **Micro** (11px mono): keyboard shortcuts and waterfall axis marks only.

### Named Rules
**The Rule of the Tabular Number.** The whole body runs with `tabular-nums`; mono data adds the slashed zero on top. Number columns align right.

**The Rule of Data in Mono.** If the value goes into a file or comes from a request, it is mono. If it is a sentence for a human, it is sans.

## Layout

The console fills the whole viewport with no page scroll: a 30px bar and, below it, two columns at 3fr/2fr (60/40). The left column stacks the services panel, as map or list (up to half the height, fitted to what it shows, scrolling from there on) and traffic (the rest); the right is entirely for the selected service or the form for a new one, for the process, or for the exchange detail. The YAML never appears on screen. The panels are separated by `gap: 1px` over a `seam` background, and each panel body scrolls on its own.

The right column has two modes, chosen with the "simple | advanced" switch in the header and remembered across visits. In **simple**, the default, the service becomes a summary line ("payments · /payments/* → 127.0.0.1:9001") and each rule shows only the switch, the name, the summarized effect, the advanced-feature mark and the probability; the process becomes a list of effective values to read. In **advanced**, everything that exists comes back: latency, drops, criteria, response, time to live, application limit, the service's fields and the process controls. Registering a service does not depend on the mode: it lives in the services panel header.

The rhythm uses the 2, 4, 8, 12 and 16px scale. A panel's side inset is 12px; controls and cells use 8px; sections inside a panel are separated by 16px and a seam. Form rows are a grid of a 108px label and a control; process settings are a grid of label, control and origin.

Below 900px the panels stack and the page scrolls; only traffic (up to 70vh) and the services panel (up to 45vh) keep their own scroll. Below 640px the services panel loses its header summary, the map reduces your app and the destinations to the port and drops the entry from the nodes, the list loses the entry column, and traffic loses the time and service columns (the intervention mark stays next to the status at any width).

## Elevation & Depth

The system is flat. Depth comes from three surface tones (`ground` < `panel` < `panel-raised`) and from 1px seams. There is no drop shadow anywhere. `box-shadow` appears only as a 1px inner stroke: the underline of the active option in the segmented control and the blue top and bottom borders of the open or focused row in the traffic list. The idle dot uses the same device as an outline.

### Named Rules
**The Rule of the Seam, Not the Shadow.** Separation is a 1px seam or a change of tone. A blurred or offset shadow does not belong to this world; a 1px inner stroke does.

**The Rule of the Opaque Sticky.** Sticky headers (the table, the detail bar, the comments notice) have an opaque `panel` or `panel-raised` background and a seam underneath; content passes behind them, never through them.

## Shapes

Almost everything is rectangular with 2px corners: buttons, fields, chips, code blocks, drafts. The exceptions have a function: the state dot is a 7px circle, the switch is a pill (8px radius) with a circular knob, and the continuous control's thumb is an 8×16px vertical bar with a 1px corner over a 2px track.

The stroke carries meaning. Solid is normal; dashed means "not yours to change right now" or "incomplete": a field locked by the environment, a locked selector, learned or derived provenance, a body cut during capture, the map's connection to a destination that is down (in red). Dotted under text marks an error from the destination.

## Components

### Buttons
Discreet and low; none of them uses a state color.
- **Shape:** a near-square rectangle (2px corners), 24px tall, 22px in the small size.
- **Default:** `panel-raised` with a `seam-strong` outline and `ink` text; hover takes the outline to `ink-3`.
- **Primary:** full ink (`ink`) with `ground` text at 600; hover goes to `ink-bright`. One per context.
- **Text:** no box, `ink-2`, hover in `ink` with a 1px underline. The destructive variant is `fault`, reserved for deleting a rule or a service.
- **Icon:** 22px square, `ink-3`, hover with a `panel-raised` background.
- **Focus:** a 1px `override` outline with a 1px offset. Disabled drops to 50% opacity.

### Chips
- **Style:** 20px tall, `seam-strong` outline, data in mono `ink-2`.
- **State:** the active filter chip (a destination filter, say) gets a blue outline, blue text and a blue background plus an internal close button.

### Cards / Containers
The console has no cards. The containers are the **panel** (a `panel` background, a 30px header with a small-caps title, an `ink-3` subtitle and tools on the right, a seam underneath) and the **inner frames**: the write error notice, the derived draft and the creation form, all with a `seam-strong` outline, 2px corners and a 12px inset, over `panel-raised` when they need to stand out from the panel.

### Inputs / Fields
- **Style:** 24px tall (22 in the small size), a recessed `ground` background, a `seam-strong` outline, 2px corners. A mono variant for any value that comes from a file. The native select loses the browser's box and gets a 1px chevron in `ink-3`.
- **Focus:** outline and border in `override`. An active filter keeps the blue border at rest.
- **Error / Disabled:** a validation error paints the border `fault` and shows the problem in 12px red underneath. Locked by the environment means dashed, transparent and with `ink-2` text, next to the padlock.

### Choice controls
- **Switch:** a 28×16px pill; on is an `override-dim` background with an `override` outline and knob.
- **Segmented control:** 20px options in `ink-3` inside a shared outline; the active one gets `panel-raised`, `ink` text and a 1px inner underline in `ink-2`. Locked becomes dashed.
- **Continuous control:** a 2px track filled up to the value in the color of what it controls (blue for probability, amber for latency, `ink-3` when the rule is off), an ink thumb and a right-aligned mono numeric field with the unit in `ink-3`.

### Navigation
There is no page navigation and no sidebar. The 30px top bar carries the name in mono 600, items separated by a vertical seam with small-caps labels and state dots, and a spacer pushing the history and learning to the right. When disconnected, the whole bar takes the `fault-dim` veil. The control panel's tabs are small-caps labels: the active one in `ink` with a 1px stroke against the header's seam.

### Services panel (signature)
The dev's mental model: your app calls the **entry**, the gateway redirects to the **destination** and applies the **rules**. The panel header carries "services" with the count in mono (or "12 of 54" when searching), the summary of active rules and destinations that are down, and, pinned to the right, the "map | list" segmented switch (the choice is kept in the browser; the default is the map), the search field (`/`, filtering by name, entry and destination in both views) and the "+ service" button (`n`), which opens the form in the panel next to it, with or without a selection, in either view.

**Map (the default view).** Three columns, "your app", "services" and "destinations", with an opaque sticky small-caps header, over the field with its 1px dot grid (`grid-dot` every 12px). Between the columns, SVG lanes with 1px curves in `seam-strong`: your app connects to each service, each service connects to its destination. The curves live only in the lanes, so they never cross a label. Nodes are 2px rectangles with a `seam-strong` outline over `panel-raised`: 34px tall with 8px of clearance up to eight services, 26px with 4px above that. Your app shows the traffic port in mono; the service shows the name at 600 and the entry in mono `ink-2` ("any" and "no destination" in `ink-3`); the destination shows the state dot, host:port in mono and health on the right ("up", "down", "no attempts", "2/5 failures"). Each destination sits at the average height of the services pointing at it, without colliding with its neighbor.

Active rule: a blue outline on the service node, the badge on the right ("402 · 100%", "+2s · 30%", "+2" for the rest) in the effect's color, and a 2px meter at the base of the node with the fraction applied. A destination that is down: outline and the word "down" in red, and the connections to it in dashed red (3/3), because the path is incomplete there. Selecting a service or a destination lights the path in blue (the node in `override-dim` with a blue outline, blue curves) and recedes the rest by tone: nodes in `panel`/`seam`/`ink-3`, curves at 30% opacity. Arrow keys walk the column, → goes from the service to its destination, ← goes back to its first service, Enter selects, Esc clears.

Scale: at 50 services or more, the services column scrolls inside the panel and your app and the destinations follow the visible window as sticky headers: they sit at their natural height while it is in view and pin to the window's edge when they would leave it, keeping their order and spacing. Only the services in view get curves, so the bundle never crosses the visible area toward hidden nodes. Below 640px, your app becomes just the port, the service loses its entry, the destination becomes the port (":9001") and the health disappears, except for "down", which stays as text.

**List (the alternative view).** One row per service, 26px, with a seam underneath and columns aligned without a table: the destination's state dot (green for up, red for down, an empty ring for no attempts), the name at 600, the entry in mono `ink-2` ("any" in `ink-3` when it matches neither host nor path), "→ destination" in mono (host:port; "no destination" in sans `ink-3`), with the word "down" in red when the destination refuses connections, and the active rule's badge aligned right, in the effect's color, over a 2px meter with a `seam-strong` track and the fraction applied. The column header is sticky and opaque.

Selecting in the list is the same gesture as in the traffic list: an `override-dim` background and blue 1px strokes above and below. The destination is a button of its own that filters the traffic by it; when filtered, it gets a blue outline and the services that do not go to it recede to `ink-3`. Arrow keys walk the column, → and ← switch between service and destination, Enter selects, Esc clears. The list scrolls on its own and overlaps nothing at 50 services or more.

### Traffic with a waterfall (signature)
Six columns and nothing more: time, method, path, status, total and the waterfall. The seventh, the service, appears only when the list is not filtered by one: with the filter on, it would repeat the same name on every row and the path takes the width instead. The intervention has no column: it is a 7px triangle pinned to the left of the status, red for synthesized and dropped, amber for delayed, with a tooltip naming the rule ("synthesized by payments/charge-declined"). Being a shape and not just a color, it survives the narrow screen with no helper box.

A table of 26px rows with a sticky small-caps header and a seam between rows. Hover in `panel-raised`, the open row in `override-dim` with blue strokes above and below. Each row ends in an 8px waterfall: the destination in `time-upstream`, injected in amber, the gateway in `time-gateway`. In the detail, the waterfall becomes 12px bands over a `ground` track with an axis and 11px mono marks.

The filters sit behind a single control. At rest the bar is the "filter" button and the active filters, each in a blue chip with its own close button; "clear" appears from the second one on. Opening the button drops a band separated by a seam with service, method, status, intervention and path, and closing it returns the screen to the list without erasing anything.

### Service panel in two modes (signature)
The "simple | advanced" switch lives to the right of the panel header, in place of the tools, just as the "map | list" switch lives in the services panel. In simple, a rule takes two lines: the header carries the switch, the name at 600, the summarized effect in mono ("402", "+200ms–900ms", "drop", joined by "·") and, on the right, only what runs out (the remaining time and the applications against the limit); below it, the probability on the continuous control. A rule that uses something only advanced can edit — latency, a drop, a time to live, an application limit or criteria beyond path and method — gets a 22px mark in `ink-3` next to the effect, whose tooltip says what it has and which takes you to advanced in one click. Nothing is hidden without warning.

## Do's and Don'ts

### Do:
- **Do** separate regions with a 1px seam (`seam`) or a step in tone; the whole console is `gap: 1px` over `seam`.
- **Do** reserve blue for selection and overrides, amber for injected time, red for drops, synthesized responses and destinations that are down, and green for the healthy dot.
- **Do** set every value from a file or a request in JetBrains Mono 12px with tabular figures and a slashed zero.
- **Do** label panels, columns and groups in 15px small caps with 0.08 to 0.1em tracking, in `ink-2` or `ink-3`.
- **Do** keep controls between 20 and 26px tall with 2px corners.
- **Do** give simple mode only what the dev adjusts at a glance, and mark every rule that uses an advanced feature.
- **Do** use a dashed stroke for locked, derived or incomplete, and give a shape beyond color to every state the narrow screen has to distinguish.
- **Do** limit animation to an exchange arriving (1.6s, `cubic-bezier(0.16, 1, 0.3, 1)`), with 120 to 160ms transitions, and turn it all off under `prefers-reduced-motion`.
- **Do** draw icons as 10px SVGs with a 1.5px stroke in `currentColor`.

### Don't:
- **Don't** use a drop or blurred shadow; only a 1px inner stroke.
- **Don't** color buttons, titles or the panel's own read failures with a state color.
- **Don't** paint an error from the destination or from the gateway red; red belongs to what the gateway did to the traffic.
- **Don't** recede elements below `ink-3`, nor with opacity over readable text.
- **Don't** introduce metric cards, decorative charts or an icon sidebar.
- **Don't** show route, upstream, override or client as the name of a concept on screen; the dev reads service, destination, rule and your app.
- **Don't** load fonts or assets over the network; everything ships embedded in the binary.
