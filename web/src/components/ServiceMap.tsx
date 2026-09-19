import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type RefObject,
} from "react";
import type { UpstreamHealth } from "../api";
import { percent } from "../format";
import type { Selection } from "../selection";
import { destinationText, healthText, type ServiceRow } from "../services";

/**
 * Up to eight services, 34px nodes with room to breathe; above that, 26px
 * nodes (the height of a list row), so that 50 services fit in a few scrolls.
 */
function geometry(n: number): { row: number; pitch: number } {
  return n > 8 ? { row: 26, pitch: 30 } : { row: 34, pitch: 42 };
}

interface DestNode {
  url: string;
  /** Wanted height: the mean of the services pointing at it. */
  mean: number;
  status: UpstreamHealth["status"];
  health: UpstreamHealth | undefined;
}

interface Model {
  rows: ServiceRow[];
  /** Vertical center of each service, in precedence order. */
  ys: number[];
  dests: DestNode[];
  /** Centers of the destinations with no scrolling: each on the mean of its services, without colliding. */
  destYs: number[];
  appY: number;
  height: number;
  row: number;
  pitch: number;
}

/**
 * Services in a queue, in precedence order. Each destination sits at the mean
 * height of the services pointing at it, pushed down when it would collide
 * with the previous one: fewer crossings without a graph library.
 */
function layout(rows: ServiceRow[]): Model {
  const { row, pitch } = geometry(rows.length);
  const ys = rows.map((_, i) => i * pitch + row / 2);
  const acc = new Map<string, { sum: number; n: number; order: number; r: ServiceRow }>();
  rows.forEach((r, i) => {
    if (!r.upstream) return;
    const a = acc.get(r.upstream);
    if (a) {
      a.sum += ys[i]!;
      a.n++;
    } else acc.set(r.upstream, { sum: ys[i]!, n: 1, order: acc.size, r });
  });
  const dests = [...acc.entries()]
    .map(([url, a]) => ({ url, mean: a.sum / a.n, order: a.order, status: a.r.status, health: a.r.health }))
    .sort((a, b) => a.mean - b.mean || a.order - b.order);
  const destYs: number[] = [];
  let prev = -Infinity;
  for (const d of dests) {
    prev = Math.max(d.mean, prev + pitch, row / 2);
    destYs.push(prev);
  }
  const height = Math.max(ys.at(-1) ?? 0, destYs.at(-1) ?? 0) + row / 2;
  return { rows, ys, dests, destYs, appY: Math.min(height / 2, row / 2 + pitch * 2), height, row, pitch };
}

/**
 * With the services column scrolling, your app and the destinations follow
 * the visible window (like sticky headers): each one stays at its natural
 * height while that height is in sight, and pins to the edge of the window
 * when it would leave it. The order and the spacing between destinations are
 * kept, so nothing overlaps.
 */
function pin(natural: number[], lo: number, hi: number, m: Model): number[] {
  const half = m.row / 2;
  const top = Math.max(half, lo + half);
  const bottom = Math.min(m.height - half, hi - half);
  if (bottom <= top) return natural;
  const ys = natural.map((y) => Math.min(Math.max(y, top), bottom));
  for (let i = 1; i < ys.length; i++) ys[i] = Math.max(ys[i]!, ys[i - 1]! + m.pitch);
  // More destinations than the window holds: they go below it, never beyond the map.
  let limit = Math.min(m.height - half, Math.max(bottom, top + (ys.length - 1) * m.pitch));
  for (let i = ys.length - 1; i >= 0; i--) {
    ys[i] = Math.min(ys[i]!, limit);
    limit = ys[i]! - m.pitch;
  }
  return ys;
}

/** The visible window of the services column, in map coordinates. */
function useVisibleWindow(fieldRef: RefObject<HTMLDivElement | null>, headRef: RefObject<HTMLDivElement | null>) {
  const [win, setWin] = useState<{ lo: number; hi: number } | null>(null);
  useLayoutEffect(() => {
    const field = fieldRef.current;
    const scroller = field?.closest<HTMLElement>(".panel__body");
    if (!field || !scroller) return;
    let frame = 0;
    const measure = () => {
      frame = 0;
      // The map coordinates start at the top of the columns, below the field's inset.
      const col = field.firstElementChild ?? field;
      const top = col.getBoundingClientRect().top - scroller.getBoundingClientRect().top - scroller.clientTop;
      const head = headRef.current?.offsetHeight ?? 0;
      // Only the area below the sticky header counts as visible.
      const lo = Math.max(0, head - top);
      const hi = scroller.clientHeight - top;
      setWin((w) => (w && w.lo === lo && w.hi === hi ? w : { lo, hi }));
    };
    const schedule = () => {
      if (!frame) frame = requestAnimationFrame(measure);
    };
    measure();
    scroller.addEventListener("scroll", schedule, { passive: true });
    const ro = new ResizeObserver(schedule);
    ro.observe(scroller);
    ro.observe(field);
    return () => {
      scroller.removeEventListener("scroll", schedule);
      ro.disconnect();
      if (frame) cancelAnimationFrame(frame);
    };
  }, [fieldRef, headRef]);
  return win;
}

/**
 * The map: your app → services → destinations, joined by curves. The services
 * column scrolls inside the panel; your app and the destinations follow the
 * visible window and the curves go with them, always in the lanes between
 * columns, never crossing labels.
 */
export function ServiceMap({
  rows,
  trafficPort,
  selection,
  stale,
  onSelect,
}: {
  rows: ServiceRow[];
  trafficPort: number | undefined;
  selection: Selection;
  stale: boolean;
  onSelect: (s: Selection) => void;
}) {
  const fieldRef = useRef<HTMLDivElement>(null);
  const headRef = useRef<HTMLDivElement>(null);
  const m = useMemo(() => layout(rows), [rows]);
  const win = useVisibleWindow(fieldRef, headRef);
  const destYs = win ? pin(m.destYs, win.lo, win.hi, m) : m.destYs;
  const appY = win ? pin([m.appY], win.lo, win.hi, m)[0]! : m.appY;
  const destY = new Map(m.dests.map((d, i) => [d.url, destYs[i]!]));
  const shortNames = useMemo(() => shortDestNames(m.dests.map((d) => d.url)), [m]);

  // With a selection, whatever is not on the selected path steps back.
  const liveRoutes = new Set<string>();
  const liveDests = new Set<string>();
  if (selection?.kind === "route") {
    liveRoutes.add(selection.name);
    const u = rows.find((r) => r.name === selection.name)?.upstream;
    if (u) liveDests.add(u);
  } else if (selection?.kind === "upstream") {
    liveDests.add(selection.name);
    for (const r of rows) if (r.upstream === selection.name) liveRoutes.add(r.name);
  }
  const dimRoute = (name: string) => selection !== null && !liveRoutes.has(name);
  const dimDest = (url: string) => selection !== null && !liveDests.has(url);
  const statusOf = new Map(m.dests.map((d) => [d.url, d.status]));

  // The selected service (by click, link, search or creation) stays in sight.
  const selKey = selection?.kind === "route" ? selection.name : "";
  useEffect(() => {
    if (!selKey) return;
    const el = [...(fieldRef.current?.querySelectorAll<HTMLElement>("[data-map-col=route]") ?? [])].find(
      (n) => n.dataset.name === selKey,
    );
    el?.scrollIntoView({ block: "nearest" });
  }, [selKey, rows.length]);

  const selectedIn = selection?.kind === "route" && rows.some((r) => r.name === selection.name);
  const style = { "--map-row": `${m.row}px`, "--map-h": `${m.height}px` } as CSSProperties;
  const curve = (y1: number, y2: number) => `M0,${y1} C50,${y1} 50,${y2} 100,${y2}`;
  // Only the services in sight (at least in part) get curves: the ones of the
  // services outside the window would cross the visible area without leading
  // to anything that can be seen.
  const shows = (y: number) => !win || (y + m.row / 2 >= win.lo && y - m.row / 2 <= win.hi);

  return (
    <div
      className={"map" + (m.row < 34 ? " map--compact" : "")}
      style={style}
      role="group"
      aria-label="Map: your app, services and destinations. Arrows to move through them, right arrow goes to the destination, Enter to select, Esc clears the selection."
      onKeyDown={onArrowKeys}
    >
      <div className="map__head" ref={headRef} aria-hidden="true">
        <span>your app</span>
        <span />
        <span>services</span>
        <span />
        <span className="map__desthead">destinations</span>
      </div>
      <div className="map__field" ref={fieldRef}>
        <div className="map__col">
          <div className="node node--app" style={{ top: appY - m.row / 2 }}>
            <span className="node__name">your app</span>
            {trafficPort ? <span className="node__meta mono">:{trafficPort}</span> : null}
          </div>
        </div>

        <svg className="map__edges" viewBox={`0 0 100 ${m.height}`} preserveAspectRatio="none" aria-hidden="true">
          {rows.map((r, i) =>
            shows(m.ys[i]!) ? (
              <path
                key={r.name}
                d={curve(appY, m.ys[i]!)}
                className={edgeClass(dimRoute(r.name), selection !== null && !dimRoute(r.name), false)}
              />
            ) : null,
          )}
        </svg>

        <div className="map__col">
          {rows.map((r, i) => {
            const isSel = selection?.kind === "route" && selection.name === r.name;
            const iv = r.intervention;
            const h = healthText(r.status, r.health);
            const cls = ["node", "node--route", isSel && "node--selected", iv && "node--ruled", dimRoute(r.name) && "is-dim"]
              .filter(Boolean)
              .join(" ");
            const label = [
              `Service ${r.name}`,
              r.entry ? `entry ${r.entry}` : "entry: any request",
              r.upstream ? `destination ${r.upstream}, ${h.long}` : "no destination",
              iv ? `active rule: ${iv.long}` : "no active rule",
            ].join(", ");
            return (
              <button
                key={r.name}
                type="button"
                className={cls}
                style={{ top: m.ys[i]! - m.row / 2 }}
                data-map-col="route"
                data-name={r.name}
                data-map-key={r.upstream ?? ""}
                tabIndex={(selectedIn ? isSel : i === 0) ? 0 : -1}
                aria-pressed={isSel}
                aria-label={label}
                title={[`${r.name} · ${r.entry || "any entry"}`, r.upstream ? `→ ${r.upstream}` : "no destination", iv?.long]
                  .filter(Boolean)
                  .join("\n")}
                onClick={() => onSelect(isSel ? null : { kind: "route", name: r.name })}
              >
                <span className="node__name">{r.name}</span>
                <span className={"node__meta mono" + (r.upstream && r.entry ? "" : " dim")}>
                  {r.upstream ? r.entry || "any" : "no destination"}
                </span>
                {iv ? (
                  <>
                    <span
                      className={`node__tag node__tag--${iv.kind}` + (stale ? " is-stale" : "")}
                      title={stale ? `No connection: state of the last update. ${iv.long}` : iv.long}
                    >
                      {iv.short}
                      {iv.chance < 1 ? ` · ${percent(iv.chance)}` : null}
                      {iv.count > 1 ? <span className="dim"> +{iv.count - 1}</span> : null}
                    </span>
                    <span
                      className={`node__meter node__meter--${iv.kind}`}
                      aria-hidden="true"
                      style={{ width: `${Math.min(1, Math.max(0, iv.chance)) * 100}%` }}
                    />
                  </>
                ) : null}
              </button>
            );
          })}
        </div>

        <svg className="map__edges" viewBox={`0 0 100 ${m.height}`} preserveAspectRatio="none" aria-hidden="true">
          {rows.map((r, i) => {
            if (!r.upstream || !shows(m.ys[i]!)) return null;
            const y2 = destY.get(r.upstream);
            if (y2 === undefined) return null;
            const dim = dimRoute(r.name) || dimDest(r.upstream);
            return (
              <path
                key={r.name}
                d={curve(m.ys[i]!, y2)}
                className={edgeClass(dim, selection !== null && !dim, statusOf.get(r.upstream) === "down")}
              />
            );
          })}
        </svg>

        <div className="map__col">
          {m.dests.map((d, i) => {
            const isSel = selection?.kind === "upstream" && selection.name === d.url;
            const text = healthText(d.status, d.health);
            const down = d.status === "down";
            const cls = ["node", "node--dest", isSel && "node--selected", down && "node--down", dimDest(d.url) && "is-dim"]
              .filter(Boolean)
              .join(" ");
            return (
              <button
                key={d.url}
                type="button"
                className={cls}
                style={{ top: destYs[i]! - m.row / 2 }}
                data-map-col="dest"
                data-map-key={d.url}
                tabIndex={-1}
                aria-pressed={isSel}
                aria-label={`Destination ${d.url}: ${text.long}. Filters the traffic by this destination.`}
                title={`${d.url}\n${text.long}\nClick to filter the traffic by this destination`}
                onClick={() => onSelect(isSel ? null : { kind: "upstream", name: d.url })}
              >
                <span className={"dot " + (down ? "dot--fault" : d.status === "up" ? "dot--healthy" : "dot--idle")} />
                <span className="node__name mono">
                  <span className="node__long">{destinationText(d.url)}</span>
                  <span className="node__short" aria-hidden="true">
                    {shortNames.get(d.url)}
                  </span>
                </span>
                <span className={"node__meta node__health mono" + (down ? " is-fault" : "")}>{text.short}</span>
              </button>
            );
          })}
          {m.dests.length === 0 ? (
            <div className="node node--ghost" style={{ top: 0 }}>
              <span className="node__name">no service has a destination</span>
            </div>
          ) : null}
        </div>
      </div>
    </div>
  );
}

/**
 * Short name of each destination for the narrow map: the port (":9001"),
 * which is what usually tells them apart in local development, or host:port
 * when the port alone would repeat.
 */
function shortDestNames(urls: string[]): Map<string, string> {
  const parts = urls.map((url) => {
    try {
      const u = new URL(url);
      return { url, host: u.hostname, port: u.port };
    } catch {
      return { url, host: url.replace(/^https?:\/\//, ""), port: "" };
    }
  });
  const byPort = new Map<string, number>();
  for (const p of parts) if (p.port) byPort.set(p.port, (byPort.get(p.port) ?? 0) + 1);
  return new Map(
    parts.map((p) => [p.url, p.port ? (byPort.get(p.port)! > 1 ? `${p.host}:${p.port}` : `:${p.port}`) : p.host]),
  );
}

function edgeClass(dim: boolean, lit: boolean, down: boolean): string | undefined {
  return [dim && "is-dim", lit && "is-selected", down && "is-down"].filter(Boolean).join(" ") || undefined;
}

/**
 * Arrows move through the map: ↑ ↓ within the column, → from the service to
 * its destination, ← from the destination to the first service pointing at
 * it. Home and End go to the ends.
 */
function onArrowKeys(e: KeyboardEvent<HTMLDivElement>) {
  const target = e.target as HTMLElement;
  const col = target.dataset.mapCol;
  if (!col) return;
  const all = (c: string) => [...e.currentTarget.querySelectorAll<HTMLElement>(`[data-map-col="${c}"]`)];
  const same = all(col);
  const i = same.indexOf(target);
  const key = target.dataset.mapKey;
  let next: HTMLElement | undefined;
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
      if (col === "route" && key) next = all("dest").find((d) => d.dataset.mapKey === key);
      break;
    case "ArrowLeft":
      if (col === "dest") next = all("route").find((r) => r.dataset.mapKey === key);
      break;
    default:
      return;
  }
  e.preventDefault();
  next?.focus();
}
