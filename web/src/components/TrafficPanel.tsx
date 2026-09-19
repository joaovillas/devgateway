import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import {
  api,
  type ConnectionState,
  type EventStream,
  type Exchange,
  type ExchangeFilter,
  type RouteResource,
  type Status,
} from "../api";
import { toApiError, useResource, type Load } from "../hooks";
import { clock, interventionTag, ms } from "../format";
import { isFiltering, matches, EMPTY_FILTER, type InterventionFilter, type ListFilter, type StatusClass } from "../exchangeFilter";
import type { Selection } from "../selection";
import { Segmented } from "./Controls";
import { ErrorNote } from "./ErrorNote";
import { HistoryDisabled, RecordingOff } from "./HistoryState";
import { CloseIcon, FilterIcon, MarkIcon } from "./Icons";
import { Panel } from "./Panel";
import { Empty, Failure, Loading } from "./States";
import type { ApiError } from "../api";

const LIMIT = 100;
const KEEP = 1000;
/** Smallest full scale for the list's waterfall: 2 ms exchanges do not become full bars. */
const MIN_SCALE_MS = 100;

interface TrafficPanelProps {
  stream: EventStream;
  connection: ConnectionState;
  status: Load<Status>;
  routes: Load<RouteResource[]>;
  /** Selection from the services panel: service (route) or destination (upstream). */
  selection: Selection;
  /** Route change from the list's selector, without changing what the detail panel shows. */
  onRoute: (name: string | null) => void;
  listFilter: ListFilter;
  onListFilter: (f: ListFilter) => void;
  /** The full filter (selection + list), in the API's format. */
  filter: ExchangeFilter;
  /** The exchange open in the detail, highlighted and brought into view. */
  openId: string | null;
  onOpen: (ex: Exchange) => void;
}

export function TrafficPanel(props: TrafficPanelProps) {
  const { stream, connection, status, selection, filter, openId, onOpen } = props;
  // epoch changes when the listing has to be read again from scratch: a
  // reconnection (missed events are not resent), a clear, a backend change or
  // a change to history.* (expose and recording may have been switched back on).
  // Re-reading by epoch keeps the list in view (reload); only a new filter goes
  // back to "loading", because the previous list is of something else.
  const [epoch, setEpoch] = useState(0);
  const filterKey = JSON.stringify(filter);
  const [page, reload] = useResource((signal) => api.listExchanges(filter, { limit: LIMIT }, signal), [filterKey]);
  const firstEpoch = useRef(epoch);
  useEffect(() => {
    if (epoch !== firstEpoch.current) reload();
  }, [epoch, reload]);
  const [live, setLive] = useState<Exchange[]>([]);
  const [more, setMore] = useState<{ items: Exchange[]; next: string; busy: boolean; error: ApiError | null }>({
    items: [],
    next: "",
    busy: false,
    error: null,
  });
  // Exchanges the server left out of the events (more than 200 per second).
  const [missed, setMissed] = useState(0);

  // A new page: the live exchanges it already carries leave `live`, but the
  // ones that arrived over SSE after the server built the page (a seq greater
  // than the page's greatest) stay, because the server does not resend them.
  const moreKey = useRef(0);
  useEffect(() => {
    moreKey.current++;
    if (page.kind === "ready") {
      const top = page.data.items.reduce((m, e) => Math.max(m, e.seq), -Infinity);
      setLive((prev) => prev.filter((e) => e.seq > top));
    } else {
      setLive([]);
    }
    setMissed(0);
    setMore({ items: [], next: page.kind === "ready" ? page.data.next : "", busy: false, error: null });
  }, [page]);

  const filterRef = useRef(filter);
  filterRef.current = filter;
  useEffect(() => {
    const bump = () => setEpoch((n) => n + 1);
    // History cleared or swapped: what arrived live before no longer exists in it.
    const reset = () => {
      setLive([]);
      bump();
    };
    const offs = [
      stream.on("hello", bump),
      stream.on("history", reset),
      stream.on("config", (ev) => {
        if ((ev.settings ?? []).some((k) => k.startsWith("history."))) reset();
      }),
      stream.on("exchanges", (ev) => {
        if (ev.dropped > 0) setMissed((n) => n + ev.dropped);
        const fresh = ev.items.filter((x) => matches(x, filterRef.current));
        if (fresh.length === 0) return;
        // The events arrive from the oldest to the newest; the list is the reverse.
        setLive((prev) => [...fresh.slice().reverse(), ...prev].slice(0, KEEP));
      }),
    ];
    return () => {
      for (const off of offs) off();
    };
  }, [stream]);

  const loadMore = () => {
    if (!more.next || more.busy) return;
    setMore((m) => ({ ...m, busy: true, error: null }));
    // The response only counts for the list that asked for it: a filter or a
    // page changed along the way discards it.
    const asked = moreKey.current;
    api.listExchanges(filter, { limit: LIMIT, cursor: more.next }).then(
      (p) => {
        if (moreKey.current !== asked) return;
        setMore((m) => ({ items: [...m.items, ...p.items], next: p.next, busy: false, error: null }));
      },
      (e: unknown) => {
        if (moreKey.current !== asked) return;
        setMore((m) => ({ ...m, busy: false, error: toApiError(e) }));
      },
    );
  };

  const liveIds = useMemo(() => new Set(live.map((e) => e.id)), [live]);
  const rows = useMemo(() => {
    if (page.kind !== "ready") return [];
    const seen = new Set<string>();
    const out: Exchange[] = [];
    for (const e of [...live, ...page.data.items, ...more.items]) {
      if (seen.has(e.id)) continue;
      seen.add(e.id);
      out.push(e);
    }
    return out.slice(0, KEEP + more.items.length);
  }, [page, live, more.items]);

  const filtered = isFiltering(props.listFilter) || selection !== null;
  const legend = (
    <span className="legend" aria-hidden="true">
      <span>
        <i style={{ background: "var(--time-upstream)" }} /> destination
      </span>
      <span>
        <i style={{ background: "var(--injected)" }} /> injected
      </span>
      <span>
        <i style={{ background: "var(--time-gateway)" }} /> gateway
      </span>
    </span>
  );

  // What the panel is receiving right now, without hiding the pause.
  const sub =
    page.kind === "error" && page.error.code === "history_disabled" ? (
      <span>disabled</span>
    ) : connection.kind === "open" ? (
      <span className="live">
        <span className="dot dot--healthy" aria-hidden="true" /> live
      </span>
    ) : (
      <span className="live live--paused" title="No connection to /api/events: the list receives no new exchanges until it reconnects">
        <span className="dot dot--fault" aria-hidden="true" /> stopped, disconnected
      </span>
    );

  let body;
  if (page.kind === "loading") {
    body = <Loading what="the history" />;
  } else if (page.kind === "error") {
    const e = page.error;
    body =
      e.code === "history_disabled" ? (
        <HistoryDisabled error={e} />
      ) : (
        <Failure what="the history" request="GET /api/exchanges" error={e} onRetry={reload} />
      );
  } else if (rows.length === 0) {
    body = !page.data.recording ? (
      <RecordingOff />
    ) : filtered ? (
      <Empty title="No exchange with this filter">
        <p>
          The history is recording, but no recorded exchange matches the filter. The ones that do will appear here on
          their own.{" "}
          <button
            type="button"
            className="link-button"
            onClick={() => {
              props.onListFilter(EMPTY_FILTER);
              props.onRoute(null);
            }}
          >
            Clear the filters
          </button>
        </p>
      </Empty>
    ) : (
      <Empty title="No exchange yet">
        <p>
          The history is on and recording in <span className="mono">{page.data.backend}</span>. Exchanges appear here
          on their own as soon as a request crosses the traffic port
          {status.kind === "ready" ? (
            <>
              {" "}
              <span className="mono">:{status.data.ports.traffic}</span>
            </>
          ) : null}
          .
        </p>
      </Empty>
    );
  } else {
    body = (
      <>
        {!page.data.recording ? <RecordingOff compact /> : null}
        {missed > 0 ? (
          <p className="notice notice--state" role="status">
            {missed} {missed === 1 ? "exchange arrived" : "exchanges arrived"} too fast for the live stream and{" "}
            {missed === 1 ? "does not appear" : "do not appear"} in the list.{" "}
            <button type="button" className="link-button" onClick={reload}>
              Read the history again
            </button>
          </p>
        ) : null}
        <TrafficTable
          rows={rows}
          fresh={liveIds}
          openId={openId}
          onOpen={onOpen}
          showRoute={selection?.kind !== "route"}
        />
        {more.next ? (
          <div className="traffic__more">
            <button type="button" className="text-button" onClick={loadMore} disabled={more.busy}>
              {more.busy ? "reading…" : `load ${LIMIT} older exchanges`}
            </button>
            {more.error ? <ErrorNote error={more.error} what="The next page did not arrive" /> : null}
          </div>
        ) : more.items.length ? (
          <p className="traffic__end dim">end of the history with this filter</p>
        ) : null}
      </>
    );
  }

  return (
    <Panel id="traffic" title="traffic" sub={sub} tools={legend}>
      <div className="traffic-panel">
        <FilterBar {...props} />
        <div className="traffic-scroll">{body}</div>
      </div>
    </Panel>
  );
}

const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"];

/** An active filter: what it says and how to leave it in one gesture. */
interface ActiveFilter {
  key: string;
  text: string;
  clear: () => void;
}

function activeFilters(
  selection: Selection,
  f: ListFilter,
  onRoute: (name: string | null) => void,
  onListFilter: (f: ListFilter) => void,
): ActiveFilter[] {
  const out: ActiveFilter[] = [];
  if (selection?.kind === "route") {
    out.push({ key: "route", text: `service ${selection.name}`, clear: () => onRoute(null) });
  } else if (selection?.kind === "upstream") {
    out.push({
      key: "upstream",
      text: `destination ${selection.name.replace(/^https?:\/\//, "")}`,
      clear: () => onRoute(null),
    });
  }
  if (f.method) out.push({ key: "method", text: `method ${f.method}`, clear: () => onListFilter({ ...f, method: "" }) });
  if (f.status !== "all") {
    out.push({ key: "status", text: `status ${f.status}`, clear: () => onListFilter({ ...f, status: "all" }) });
  }
  if (f.intervened !== "all") {
    out.push({
      key: "intervened",
      text: f.intervened === "yes" ? "with intervention" : "without intervention",
      clear: () => onListFilter({ ...f, intervened: "all" }),
    });
  }
  if (f.path.trim()) {
    out.push({ key: "path", text: `path with ${f.path.trim()}`, clear: () => onListFilter({ ...f, path: "" }) });
  }
  return out;
}

/**
 * The filters sit behind a single control: at rest the bar is the "filter"
 * button and the active filters, each one removable on its own. The controls
 * (service, method, status, intervention and path) only take up the screen
 * while the panel is open.
 */
function FilterBar(props: TrafficPanelProps) {
  const { selection, onRoute, listFilter: f, onListFilter } = props;
  const [open, setOpen] = useState(false);
  const panelId = useId();
  const chips = activeFilters(selection, f, onRoute, onListFilter);

  return (
    <div className="filters" role="search" aria-label="Filter the traffic">
      <div className="filters__bar">
        <button
          type="button"
          className={"button button--sm filters__toggle" + (open ? " filters__toggle--on" : "")}
          aria-expanded={open}
          aria-controls={open ? panelId : undefined}
          onClick={() => setOpen((v) => !v)}
        >
          <FilterIcon /> filter
        </button>
        {chips.map((c) => (
          <span key={c.key} className="chip chip--override">
            {c.text}
            <button type="button" onClick={c.clear} aria-label={`Remove the ${c.text} filter`}>
              <CloseIcon />
            </button>
          </span>
        ))}
        {chips.length > 1 ? (
          <button
            type="button"
            className="text-button"
            onClick={() => {
              onListFilter(EMPTY_FILTER);
              onRoute(null);
            }}
          >
            clear
          </button>
        ) : null}
      </div>
      {open ? <FilterFields {...props} id={panelId} /> : null}
    </div>
  );
}

/** The controls of the contract: service (or the destination from the services panel), method, status, intervention and path. */
function FilterFields({
  routes,
  selection,
  onRoute,
  listFilter: f,
  onListFilter,
  id,
}: TrafficPanelProps & { id: string }) {
  const [path, setPath] = useState(f.path);
  // Filter cleared from outside (a chip removed or "clear"): the field follows.
  useEffect(() => setPath(f.path), [f.path]);
  useEffect(() => {
    if (path === f.path) return;
    const t = window.setTimeout(() => onListFilter({ ...f, path }), 250);
    return () => window.clearTimeout(t);
  }, [path, f, onListFilter]);

  const names = routes.kind === "ready" ? routes.data.map((r) => r.route.name) : [];
  const routeName = selection?.kind === "route" ? selection.name : "";

  return (
    <div className="filters__fields" id={id}>
      {selection?.kind === "upstream" ? (
        <span className="filters__item">
          <span className="filters__label">destination</span>
          <span className="mono">{selection.name.replace(/^https?:\/\//, "")}</span>
          <button type="button" className="text-button" onClick={() => onRoute(null)}>
            remove
          </button>
        </span>
      ) : (
        <label className="filters__item">
          <span className="filters__label">service</span>
          <select
            className={"input input--sm select" + (routeName ? " select--on" : "")}
            value={routeName}
            onChange={(e) => onRoute(e.target.value || null)}
          >
            <option value="">all</option>
            {routeName && !names.includes(routeName) ? <option value={routeName}>{routeName}</option> : null}
            {names.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
        </label>
      )}
      <label className="filters__item">
        <span className="filters__label">method</span>
        <select
          className={"input input--sm select" + (f.method ? " select--on" : "")}
          value={f.method}
          onChange={(e) => onListFilter({ ...f, method: e.target.value })}
        >
          <option value="">all</option>
          {METHODS.map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
        </select>
      </label>
      <span className="filters__item">
        <span className="filters__label" aria-hidden="true">
          status
        </span>
        <Segmented<StatusClass>
          label="Status"
          value={f.status}
          onChange={(status) => onListFilter({ ...f, status })}
          options={[
            { value: "all", label: "all" },
            { value: "2xx", label: "2xx" },
            { value: "3xx", label: "3xx" },
            { value: "4xx", label: "4xx" },
            { value: "5xx", label: "5xx" },
          ]}
        />
      </span>
      <span className="filters__item">
        <span className="filters__label" aria-hidden="true">
          intervention
        </span>
        <Segmented<InterventionFilter>
          label="Intervention"
          value={f.intervened}
          onChange={(intervened) => onListFilter({ ...f, intervened })}
          options={[
            { value: "all", label: "all" },
            { value: "yes", label: "with" },
            { value: "no", label: "without" },
          ]}
        />
      </span>
      <label className="filters__item filters__item--grow">
        <span className="filters__label">path</span>
        <input
          className={"input input--sm input--mono" + (f.path ? " input--on" : "")}
          type="search"
          value={path}
          placeholder="contains…"
          spellCheck={false}
          onChange={(e) => setPath(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") onListFilter({ ...f, path });
          }}
        />
      </label>
    </div>
  );
}

function TrafficTable({
  rows,
  fresh,
  openId,
  onOpen,
  showRoute,
}: {
  rows: Exchange[];
  fresh: Set<string>;
  openId: string | null;
  onOpen: (ex: Exchange) => void;
  /** With a route filter, the column would repeat the same name on every row and goes away; the path takes the width. */
  showRoute: boolean;
}) {
  const body = useRef<HTMLTableSectionElement>(null);
  const openRow = useRef<HTMLTableRowElement>(null);
  const lastOpen = useRef<string | null>(null);
  useEffect(() => {
    // Scrolls only the list (not the page) to the open exchange, allowing for the fixed header.
    const row = openRow.current;
    const box = row?.closest(".traffic-scroll");
    if (row && box) {
      const head = (box.querySelector("thead") as HTMLElement | null)?.offsetHeight ?? 0;
      const r = row.getBoundingClientRect();
      const b = box.getBoundingClientRect();
      if (r.top < b.top + head) box.scrollTop -= b.top + head - r.top;
      else if (r.bottom > b.bottom) box.scrollTop += r.bottom - b.bottom;
    }
    // Detail closed (Esc or the button): the focus, which was in it and vanished, goes back to the exchange's row.
    const closed = lastOpen.current;
    lastOpen.current = openId;
    if (!openId && closed && (document.activeElement === document.body || document.activeElement === null)) {
      const i = rows.findIndex((r) => r.id === closed);
      (body.current?.children[i] as HTMLElement | undefined)?.focus();
    }
  }, [openId]);
  // A linear scale shared by every visible row, with a minimum full scale.
  const max = Math.max(MIN_SCALE_MS, ...rows.map((r) => r.timing.totalMs));
  const focusIndex = Math.max(0, rows.findIndex((r) => r.id === openId));

  const onKey = (e: KeyboardEvent<HTMLTableRowElement>, i: number) => {
    let next = -1;
    if (e.key === "ArrowDown") next = Math.min(rows.length - 1, i + 1);
    else if (e.key === "ArrowUp") next = Math.max(0, i - 1);
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = rows.length - 1;
    else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onOpen(rows[i]!);
      return;
    }
    if (next < 0) return;
    e.preventDefault();
    const el = body.current?.children[next] as HTMLElement | undefined;
    el?.focus();
    // With an exchange open, moving through the list opens each row's exchange.
    if (openId) onOpen(rows[next]!);
  };

  return (
    <table className="traffic">
      <caption className="sr-only">
        Captured exchanges, from the newest to the oldest. The arrows move through the rows and Enter opens the detail.
      </caption>
      <colgroup>
        <col className="traffic__c-time" />
        <col className="traffic__c-method" />
        <col />
        {showRoute ? <col className="traffic__c-route" /> : null}
        <col className="traffic__c-status" />
        <col className="traffic__c-total" />
        <col className="traffic__c-fall" />
      </colgroup>
      <thead>
        <tr>
          <th scope="col" className="traffic__time">
            time
          </th>
          <th scope="col">method</th>
          <th scope="col">path</th>
          {showRoute ? (
            <th scope="col" className="traffic__route">
              service
            </th>
          ) : null}
          <th scope="col" className="num">
            status
          </th>
          <th scope="col" className="num">
            total
          </th>
          <th scope="col" className="traffic__fallhead">
            <span className="traffic__fallword">time</span>{" "}
            <span className="traffic__scale mono">0–{ms(max)}</span>
          </th>
        </tr>
      </thead>
      <tbody ref={body}>
        {rows.map((e, i) => {
          const tag = interventionTag(e);
          const status = e.status ? String(e.status) : "—";
          // Red only for what the gateway did (a drop or a synthesized response);
          // a 500 from the upstream is not an intervention and keeps the text
          // color, underlined.
          const fault = e.outcome === "dropped" || e.outcome === "synthesized";
          // The error from the gateway itself is not the upstream's: it stays in the label's gray.
          const gatewayError = e.outcome === "gateway";
          const upstreamError = !fault && !gatewayError && (e.status ?? 0) >= 500;
          const open = e.id === openId;
          return (
            <tr
              key={e.id}
              ref={open ? openRow : undefined}
              tabIndex={i === focusIndex ? 0 : -1}
              aria-current={open ? "true" : undefined}
              onClick={() => onOpen(e)}
              onKeyDown={(ev) => onKey(ev, i)}
              className={
                [fresh.has(e.id) ? "is-new" : "", open ? "is-open" : "", tag ? "is-intervened" : ""].join(" ").trim() ||
                undefined
              }
            >
              <td className="mono dim traffic__time">{clock(e.start)}</td>
              <td className="mono">{e.method}</td>
              <td className="mono" title={e.path + (e.query ? "?" + e.query : "")}>
                {e.path}
                {e.query ? <span className="dim">?{e.query}</span> : null}
              </td>
              {showRoute ? (
                <td className={"traffic__route" + (e.route ? "" : " dim")} title={e.route ?? undefined}>
                  {e.route ?? "no service"}
                </td>
              ) : null}
              <td
                className={
                  "mono num" +
                  (fault
                    ? " status--fault"
                    : gatewayError
                      ? " status--gateway"
                      : upstreamError
                        ? " status--upstream-error"
                        : "")
                }
                title={gatewayError ? "error from the gateway itself" : upstreamError ? "error from the destination" : undefined}
              >
                {/* The intervention is a mark next to the status, not a column:
                    the triangle gives shape beyond the color and the hint names the rule. */}
                {tag ? <InterventionMark ex={e} tag={tag} /> : null}
                {status}
              </td>
              <td className="mono num">{ms(e.timing.totalMs)}</td>
              <td>
                <Waterfall ex={e} max={max} />
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

/**
 * The intervention's mark, next to the status: a triangle in the color of what
 * the gateway did ("synthesized by payments/charge-declined"). It works as a
 * shape, so it stays distinguishable without color and on a narrow screen.
 */
function InterventionMark({ ex, tag }: { ex: Exchange; tag: NonNullable<ReturnType<typeof interventionTag>> }) {
  const by = ex.override ? ` by ${ex.override}` : ex.route ? ` by a rule of ${ex.route}` : "";
  const title = `${tag.label}${by}`;
  return (
    <span className={`imark imark--${tag.tone}`} role="img" aria-label={title} title={title}>
      <MarkIcon />
    </span>
  );
}

/** One segment per quantity, in the order of the path: upstream, injected delay, overhead. */
function Waterfall({ ex, max }: { ex: Exchange; max: number }) {
  const t = ex.timing;
  const pct = (v: number) => `${(v / max) * 100}%`;
  const label = `destination ${ms(t.upstreamMs)}, injected ${ms(t.injectedMs)}, gateway ${ms(t.gatewayMs)}`;
  return (
    <div className="fall" role="img" aria-label={label} title={label}>
      {t.upstreamMs > 0 ? <span className="fall__seg fall__seg--upstream" style={{ width: pct(t.upstreamMs) }} /> : null}
      {t.injectedMs > 0 ? <span className="fall__seg fall__seg--injected" style={{ width: pct(t.injectedMs) }} /> : null}
      {t.gatewayMs > 0 ? <span className="fall__seg fall__seg--gateway" style={{ width: pct(t.gatewayMs) }} /> : null}
    </div>
  );
}
