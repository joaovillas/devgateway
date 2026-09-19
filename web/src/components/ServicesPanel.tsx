import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import type { RouteResource, UpstreamHealth } from "../api";
import type { Load } from "../hooks";
import { useNow } from "../hooks";
import { percent } from "../format";
import type { Selection } from "../selection";
import {
  destinationText,
  filterRows,
  hasTicking,
  healthText,
  searchTerms,
  serviceRows,
  type ServiceRow,
} from "../services";
import { useStale } from "../live";
import { Segmented } from "./Controls";
import { PlusIcon } from "./Icons";
import { Panel } from "./Panel";
import { ServiceMap } from "./ServiceMap";
import { Empty, Failure, Loading } from "./States";

interface ServicesPanelProps {
  routes: Load<RouteResource[]>;
  upstreams: Load<UpstreamHealth[]>;
  /** Traffic port, shown on the "your app" node of the map. */
  trafficPort: number | undefined;
  selection: Selection;
  onSelect: (s: Selection) => void;
  onRetry: () => void;
  /** The new-service form is open in the panel beside this one. */
  creating: boolean;
  onCreate: () => void;
}

export type ServicesView = "map" | "list";

const VIEW_KEY = "gateway.panel.services";
const VIEW_OPTIONS: { value: ServicesView; label: string }[] = [
  { value: "map", label: "map" },
  { value: "list", label: "list" },
];

/** The view remembered between sessions; the map is the default. */
function readView(): ServicesView {
  try {
    return window.localStorage.getItem(VIEW_KEY) === "list" ? "list" : "map";
  } catch {
    return "map";
  }
}

/** Outside text fields and dialogs: where the single-key shortcuts hold. */
function isTyping(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el) return false;
  return el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName) || el.closest("[role=alertdialog]") !== null;
}

/**
 * The services: each one is a route, with the entry through which the app
 * calls the gateway, the destination where it forwards to and the rule that
 * is intervening right now. Two views of the same set: the map (your app →
 * services → destinations, the default) and the dense list. Search and the
 * new-service button stay pinned at the top and hold for both.
 */
export function ServicesPanel({
  routes,
  upstreams,
  trafficPort,
  selection,
  onSelect,
  onRetry,
  creating,
  onCreate,
}: ServicesPanelProps) {
  const [query, setQuery] = useState("");
  const [view, setViewRaw] = useState<ServicesView>(readView);
  const searchRef = useRef<HTMLInputElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const data = routes.kind === "ready" ? routes.data : null;
  // Instant when the live state arrived: the base of the local TTL arithmetic.
  const at = useMemo(() => Date.now(), [data]);
  const now = useNow(data ? hasTicking(data) : false);
  const stale = useStale();

  const setView = useCallback((v: ServicesView) => {
    setViewRaw(v);
    try {
      window.localStorage.setItem(VIEW_KEY, v);
    } catch {
      // Without storage, the choice holds for this tab only.
    }
  }, []);

  // "/" goes to the search and "n" opens the new-service form, from anywhere outside a field.
  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey || isTyping(e.target)) return;
      if (e.key === "/") {
        e.preventDefault();
        searchRef.current?.focus();
        searchRef.current?.select();
      } else if (e.key === "n") {
        e.preventDefault();
        onCreate();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onCreate]);

  const health = upstreams.kind === "ready" ? upstreams.data : [];
  const rows: ServiceRow[] = useMemo(() => (data ? serviceRows(data, health, now, at) : []), [data, health, now, at]);
  const terms = searchTerms(query);
  const shown = filterRows(rows, terms);

  let sub;
  let body;
  if (routes.kind === "loading") {
    body = <Loading what="the services" />;
  } else if (routes.kind === "error") {
    body = <Failure what="the services" request="GET /api/routes" error={routes.error} onRetry={onRetry} />;
  } else if (rows.length === 0) {
    body = (
      <Empty title="No service registered">
        <p>
          Add the first one under <strong>+ service</strong>: a name, the entry through which your app calls the gateway
          and the destination where it forwards to.
        </p>
      </Empty>
    );
  } else {
    const ruled = rows.filter((r) => r.intervention).length;
    const down = new Set(rows.filter((r) => r.status === "down").map((r) => r.upstream)).size;
    sub = (
      <>
        <span className="mono">{terms.length ? `${shown.length} of ${rows.length}` : rows.length}</span>
        <span className="svc__subx">
          {ruled > 0 ? ` · ${ruled} with an active rule` : null}
          {down > 0 ? ` · ${down} ${down === 1 ? "destination down" : "destinations down"}` : null}
        </span>
      </>
    );
    const notice =
      upstreams.kind === "error" ? (
        <p className="notice notice--state svc__notice">
          State of the destinations unknown: <span className="mono">GET /api/upstreams</span> answered{" "}
          <span className="mono">{upstreams.error.code}</span>.
        </p>
      ) : null;
    const none =
      shown.length === 0 ? (
        <Empty title={`No service with “${query.trim()}”`}>
          <p>
            The search looks at the name, the entry and the destination.{" "}
            <button type="button" className="link-button" onClick={() => setQuery("")}>
              Clear the search
            </button>
          </p>
        </Empty>
      ) : null;
    body = (
      <div className={view === "map" ? "svc svc--map" : "svc"} ref={bodyRef}>
        {notice}
        {view === "map" ? (
          (none ?? <ServiceMap rows={shown} trafficPort={trafficPort} selection={selection} stale={stale} onSelect={onSelect} />)
        ) : (
          <ServiceList rows={shown} none={none} selection={selection} stale={stale} onSelect={onSelect} />
        )}
      </div>
    );
  }

  const tools = (
    <>
      <Segmented<ServicesView> value={view} options={VIEW_OPTIONS} onChange={setView} label="View of the services" />
      <span className="search">
        <input
          ref={searchRef}
          className={"input input--sm search__input" + (query ? " input--on" : "")}
          type="search"
          value={query}
          placeholder="search"
          spellCheck={false}
          aria-label="Search services by name, entry or destination"
          aria-keyshortcuts="/"
          title="Search by name, entry or destination (key /)"
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape" && query) {
              e.preventDefault();
              setQuery("");
            } else if (e.key === "Escape") {
              e.currentTarget.blur();
            } else if (e.key === "ArrowDown") {
              e.preventDefault();
              bodyRef.current?.querySelector<HTMLElement>("[data-col=main], [data-map-col=route]")?.focus();
            } else if (e.key === "Enter" && shown[0] && terms.length) {
              e.preventDefault();
              onSelect({ kind: "route", name: shown[0].name });
            }
          }}
        />
        {query ? null : <kbd className="search__kbd" aria-hidden="true">/</kbd>}
      </span>
      <button
        type="button"
        className={"button button--sm svc__new" + (creating ? " is-on" : "")}
        onClick={onCreate}
        aria-keyshortcuts="n"
        title="Add a service (key n)"
      >
        <PlusIcon /> service <kbd aria-hidden="true">n</kbd>
      </button>
    </>
  );

  return (
    <Panel id="services" title="services" sub={sub} tools={tools}>
      {body}
    </Panel>
  );
}

/** The list view: one row per service, with aligned columns. */
function ServiceList({
  rows: shown,
  none,
  selection,
  stale,
  onSelect,
}: {
  rows: ServiceRow[];
  none: ReactNode;
  selection: Selection;
  stale: boolean;
  onSelect: (s: Selection) => void;
}) {
  const listRef = useRef<HTMLUListElement>(null);

  // The selected row (by click, link or creation) stays in sight in the list.
  const selKey = selection ? `${selection.kind}:${selection.name}` : "";
  useEffect(() => {
    if (selection?.kind !== "route") return;
    const li = [...(listRef.current?.children ?? [])].find((el) => (el as HTMLElement).dataset.name === selection.name);
    li?.scrollIntoView({ block: "nearest" });
    // The list only needs to scroll when the selection changes or when the row appears.
  }, [selKey, shown.length]);

  return (
    <>
      <div className="svc__head" aria-hidden="true">
        <span className="svc__main">
          <span />
          <span>service</span>
          <span className="svc__entry">entry</span>
        </span>
        <span>destination</span>
        <span className="svc__rulehead">rule</span>
      </div>
      {none ?? (
        <ul
          ref={listRef}
          className="svc__list"
          aria-label="Services. Arrows to move through them, Enter to select, right arrow goes to the destination, Esc clears the selection."
          onKeyDown={onArrowKeys}
        >
          {shown.map((r, i) => (
            <Row
              key={r.name}
              row={r}
              tabbable={
                selection?.kind === "route"
                  ? r.name === selection.name || (i === 0 && !shown.some((x) => x.name === selection.name))
                  : i === 0
              }
              selection={selection}
              stale={stale}
              onSelect={onSelect}
            />
          ))}
        </ul>
      )}
    </>
  );
}

function Row({
  row: r,
  tabbable,
  selection,
  stale,
  onSelect,
}: {
  row: ServiceRow;
  tabbable: boolean;
  selection: Selection;
  stale: boolean;
  onSelect: (s: Selection) => void;
}) {
  const isSel = selection?.kind === "route" && selection.name === r.name;
  const destSel = selection?.kind === "upstream" && selection.name === r.upstream;
  // With a destination selected, the services that do not go to it step back.
  const dim = selection?.kind === "upstream" && !destSel;
  const iv = r.intervention;
  const h = healthText(r.status, r.health);
  const down = r.status === "down";
  const label = [
    `Service ${r.name}`,
    r.entry ? `entry ${r.entry}` : "entry: any request",
    r.upstream ? `destination ${r.upstream}, ${h.long}` : "no destination",
    iv ? `active rule: ${iv.long}` : "no active rule",
  ].join(", ");
  const cls = ["svc__row", isSel && "is-selected", dim && "is-dim", down && "is-down"].filter(Boolean).join(" ");

  return (
    <li className={cls} data-name={r.name} onClick={() => onSelect(isSel ? null : { kind: "route", name: r.name })}>
      <button
        type="button"
        className="svc__main"
        data-col="main"
        tabIndex={tabbable ? 0 : -1}
        aria-pressed={isSel}
        aria-label={label}
        title={r.upstream ? `${r.name} → ${r.upstream}\n${h.long}` : `${r.name}: no destination`}
      >
        <span
          className={
            "dot " + (!r.upstream ? "dot--none" : down ? "dot--fault" : r.status === "up" ? "dot--healthy" : "dot--idle")
          }
        />
        <span className="svc__name">{r.name}</span>
        <span className={"svc__entry mono" + (r.entry ? "" : " dim")}>{r.entry || "any"}</span>
      </button>
      {r.upstream ? (
        <button
          type="button"
          className={"svc__dest mono" + (destSel ? " is-on" : "")}
          data-col="dest"
          tabIndex={-1}
          aria-pressed={destSel}
          aria-label={`Filter the traffic by the destination ${r.upstream}: ${h.long}`}
          title={`${r.upstream}\n${h.long}\nClick to filter the traffic by this destination`}
          onClick={(e) => {
            e.stopPropagation();
            onSelect(destSel ? null : { kind: "upstream", name: r.upstream! });
          }}
        >
          <span className="svc__arrow" aria-hidden="true">
            →
          </span>
          <span className="svc__host">{destinationText(r.upstream)}</span>
          {down ? <span className="svc__down">down</span> : null}
        </button>
      ) : (
        <span className="svc__dest svc__dest--none">no destination</span>
      )}
      <span className="svc__rule">
        {iv ? (
          <span
            className={`svc__tag svc__tag--${iv.kind}` + (stale ? " is-stale" : "")}
            title={stale ? `No connection: state of the last update. ${iv.long}` : iv.long}
          >
            {iv.short}
            {iv.chance < 1 ? ` · ${percent(iv.chance)}` : null}
            {iv.count > 1 ? <span className="dim"> +{iv.count - 1}</span> : null}
            <span
              className="svc__meter"
              aria-hidden="true"
              style={{ width: `${Math.min(1, Math.max(0, iv.chance)) * 100}%` }}
            />
          </span>
        ) : null}
      </span>
    </li>
  );
}

/**
 * Arrows move through the list: ↑ ↓ within the same column (service or
 * destination), → from the service to the destination of the row, ← back.
 * Home and End go to the ends.
 */
function onArrowKeys(e: KeyboardEvent<HTMLUListElement>) {
  const target = e.target as HTMLElement;
  const col = target.dataset.col;
  if (!col) return;
  const inCol = (c: string) => [...e.currentTarget.querySelectorAll<HTMLElement>(`[data-col="${c}"]`)];
  const same = inCol(col);
  const i = same.indexOf(target);
  const row = target.closest("li");
  let next: HTMLElement | null | undefined;
  switch (e.key) {
    case "ArrowDown":
      next = same[Math.min(same.length - 1, i + 1)];
      break;
    case "ArrowUp":
      next = same[Math.max(0, i - 1)];
      break;
    case "Home":
      next = same[0];
      break;
    case "End":
      next = same.at(-1);
      break;
    case "ArrowRight":
      if (col === "main") next = row?.querySelector<HTMLElement>("[data-col=dest]");
      break;
    case "ArrowLeft":
      if (col === "dest") next = row?.querySelector<HTMLElement>("[data-col=main]");
      break;
    default:
      return;
  }
  e.preventDefault();
  next?.focus();
}
