import { useCallback, useEffect, useId, useRef, useState } from "react";
import { api, isApiError, type Exchange, type ExchangeFilter, type Message, type RouteResource } from "../api";
import { decodeMessage, type DecodedBody } from "../body";
import type { CommentsGuard } from "../commentsGuard";
import { describeFilter } from "../exchangeFilter";
import { bytes, clock, ms, num } from "../format";
import { toApiError, useResource, type Load } from "../hooks";
import { DeriveDraft } from "./DeriveDraft";
import { ErrorNote } from "./ErrorNote";
import { ArrowIcon, CloseIcon, PlusIcon } from "./Icons";
import { Empty, Loading } from "./States";
import type { ApiError } from "../api";

export interface OpenedExchange {
  id: string;
  /** The full exchange already in hand (the result of navigating); without it, the detail reads it by id. */
  data?: Exchange;
}

interface ExchangeDetailProps {
  opened: OpenedExchange;
  /** The list's active filter: item-by-item navigation respects it. */
  filter: ExchangeFilter;
  onOpen: (ex: OpenedExchange) => void;
  onClose: () => void;
  routes: Load<RouteResource[]>;
  guard: CommentsGuard;
  onShowRoute: (name: string) => void;
}

type Dir = "newer" | "older";

/** The open exchange: full request and response, time broken down and navigation. */
export function ExchangeDetail({ opened, filter, onOpen, onClose, routes, guard, onShowRoute }: ExchangeDetailProps) {
  const { id, data } = opened;
  const [ex, reload] = useResource<Exchange>(
    (signal) => (data && data.id === id ? Promise.resolve(data) : api.getExchange(id, signal)),
    [id],
  );
  const [nav, setNav] = useState<{ dir: Dir; busy: boolean; message?: string; error?: ApiError } | null>(null);
  const [drafting, setDrafting] = useState(false);
  const [created, setCreated] = useState<{ route: string; name: string; enabled: boolean } | null>(null);
  const root = useRef<HTMLDivElement>(null);

  // A new exchange: the previous one's draft and notices do not apply to it.
  useEffect(() => {
    setDrafting(false);
    setCreated(null);
    setNav(null);
  }, [id]);

  // An exchange opened by id alone (the origin of an override, for example):
  // once the exchange is read, whoever opened it learns its route.
  useEffect(() => {
    // Only the exchange of the current id: the one left over from an earlier opening does not come back.
    if (ex.kind === "ready" && !data && ex.data.id === id) onOpen({ id: ex.data.id, data: ex.data });
  }, [ex, data, id, onOpen]);

  // When narrow, the panels stack and the detail sits below the list: opening
  // an exchange scrolls the page to it. It waits for the exchange to load:
  // before that the page is still too short to get there.
  const ready = ex.kind !== "loading";
  useEffect(() => {
    if (ready && window.matchMedia("(max-width: 900px)").matches) {
      root.current?.closest(".panel")?.scrollIntoView({ block: "start" });
    }
  }, [id, ready]);

  // One navigation at a time; switching exchange or closing the detail cancels
  // it, so a late response does not reopen anything or arrive out of order.
  const navCtl = useRef<AbortController | null>(null);
  useEffect(
    () => () => {
      navCtl.current?.abort();
      navCtl.current = null;
    },
    [id],
  );
  const go = useCallback(
    (dir: Dir) => {
      if (navCtl.current) return;
      const ctl = new AbortController();
      navCtl.current = ctl;
      setNav({ dir, busy: true });
      (dir === "older" ? api.olderExchange(id, filter, ctl.signal) : api.newerExchange(id, filter, ctl.signal)).then(
        (next) => {
          if (ctl.signal.aborted) return;
          navCtl.current = null;
          setNav(null);
          onOpen({ id: next.id, data: next });
        },
        (e: unknown) => {
          if (ctl.signal.aborted) return;
          navCtl.current = null;
          const err = toApiError(e);
          setNav(
            isApiError(err, "no_more")
              ? { dir, busy: false, message: err.message }
              : { dir, busy: false, error: err },
          );
        },
      );
    },
    [id, filter, onOpen],
  );

  // j and k move through the exchanges from anywhere on the screen, outside text fields.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey) return;
      if (e.key !== "j" && e.key !== "k") return;
      if (navCtl.current) return; // a navigation is already on its way
      const t = e.target as HTMLElement | null;
      if (t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return;
      if (t?.closest("[role=alertdialog]")) return;
      e.preventDefault();
      go(e.key === "j" ? "older" : "newer");
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [go]);

  const filterText = describeFilter(filter);
  const busy = nav?.busy === true;

  const bar = (
    <div className="xd__bar">
      <span className="xd__nav" role="group" aria-label="Move through the exchanges">
        <button
          type="button"
          className="button button--sm"
          onClick={() => go("newer")}
          disabled={busy}
          aria-keyshortcuts="k"
          title="Newer exchange with the active filter (k key)"
        >
          <ArrowIcon dir="up" /> newer <kbd>k</kbd>
        </button>
        <button
          type="button"
          className="button button--sm"
          onClick={() => go("older")}
          disabled={busy}
          aria-keyshortcuts="j"
          title="Older exchange with the active filter (j key)"
        >
          <ArrowIcon dir="down" /> older <kbd>j</kbd>
        </button>
      </span>
      <span className="xd__navnote" role="status">
        {busy ? "reading…" : nav?.message ? nav.message : filterText ? `filter: ${filterText}` : "no filter"}
      </span>
      <button type="button" className="icon-button xd__close" onClick={onClose} aria-label="Close the exchange">
        <CloseIcon />
      </button>
    </div>
  );

  let body;
  if (ex.kind === "loading") {
    body = <Loading what="the exchange" />;
  } else if (ex.kind === "error") {
    const e = ex.error;
    body =
      e.code === "not_found" ? (
        <Empty title="The exchange left the history">
          <p>
            <span className="mono">…{id.slice(-8)}</span> is no longer in the backend: it keeps a limited number of
            exchanges and discards the oldest. The neighboring ones can still be moved through.
          </p>
        </Empty>
      ) : e.code === "history_disabled" ? (
        <Empty title="History disabled">
          <p>{e.message}</p>
        </Empty>
      ) : (
        <div className="xd__pad">
          <ErrorNote error={e} what={`GET /api/exchanges/${id} failed`} />
          <button type="button" className="link-button" onClick={reload}>
            Try again
          </button>
        </div>
      );
  } else {
    const x = ex.data;
    body = (
      <>
        <Summary x={x} onShowRoute={onShowRoute} />
        <DeriveBlock
          x={x}
          routes={routes}
          drafting={drafting}
          created={created}
          onDraft={() => {
            setCreated(null);
            setDrafting(true);
          }}
          onShowRoute={onShowRoute}
        />
        {drafting ? (
          <DeriveDraft
            exchange={x}
            routes={routes}
            guard={guard}
            onCancel={() => setDrafting(false)}
            onCreated={(c) => {
              setDrafting(false);
              setCreated(c);
            }}
          />
        ) : null}
        <TimeLanes x={x} />
        <MessageSection title="request" m={x.request} x={x} kind="request" />
        <MessageSection title="response" m={x.response} x={x} kind="response" />
        <Routing x={x} />
      </>
    );
  }

  return (
    <div
      className="xd"
      ref={root}
      onKeyDown={(e) => {
        if (e.key !== "Escape" || e.defaultPrevented) return;
        const t = e.target as HTMLElement;
        if (/^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName)) return;
        e.preventDefault();
        onClose();
      }}
    >
      {bar}
      {nav?.error ? (
        <div className="xd__pad">
          <ErrorNote error={nav.error} onDismiss={() => setNav(null)} what="The navigation failed" />
        </div>
      ) : null}
      {body}
    </div>
  );
}

/** "charge-declined rule of the payments service", from the override "payments/charge-declined". */
function ruleName(x: Exchange): string {
  const o = x.override ?? "";
  const route = x.route ?? "";
  const name = route && o.startsWith(route + "/") ? o.slice(route.length + 1) : o;
  return route ? `${name} rule of the ${route} service` : `${name} rule`;
}

function outcomeText(x: Exchange): { text: string; fault: boolean } {
  const by = x.override ? ` by the ${ruleName(x)}` : "";
  switch (x.outcome) {
    case "synthesized":
      return { text: `response synthesized${by}`, fault: true };
    case "dropped":
      return {
        text: `connection dropped${by}${x.dropMode === "stream_reset" ? " (HTTP/2 stream canceled)" : ""}`,
        fault: true,
      };
    case "gateway":
      return { text: "error from the gateway itself", fault: false };
    default:
      return {
        text: (x.status ?? 0) >= 500 ? "error from the destination, passed on without intervention" : "answered by the destination",
        fault: false,
      };
  }
}

function Summary({ x, onShowRoute }: { x: Exchange; onShowRoute: (name: string) => void }) {
  const o = outcomeText(x);
  const delayed = (x.interventions ?? []).includes("delayed");
  return (
    <header className="xd__head">
      <p className="xd__req">
        <span className="xd__method">{x.method}</span> <span className="xd__path">{x.path}</span>
        {x.query ? <span className="dim">?{x.query}</span> : null}
      </p>
      <p className="xd__verdict">
        <span className={"xd__status mono" + (o.fault ? " status--fault" : "")}>{x.status ? x.status : "no status"}</span>
        <span className={o.fault ? "tone-drop" : undefined}>{o.text}</span>
        {delayed ? <span className="tone-injected">delayed {ms(x.timing.injectedMs)}</span> : null}
        <span className="dim mono">
          {ms(x.timing.totalMs)} · {clock(x.start)}
        </span>
      </p>
      {x.error ? <p className="xd__error mono">{x.error}</p> : null}
      <p className="xd__meta dim">
        {x.route ? (
          <>
            service{" "}
            <button type="button" className="text-button text-button--inline mono" onClick={() => onShowRoute(x.route!)}>
              {x.route}
            </button>
          </>
        ) : (
          "no service matched the entry"
        )}
        {x.upstream ? (
          <>
            {" · "}destination <span className="mono">{x.upstream}</span>
          </>
        ) : null}
        {" · "}exchange <span className="mono" title={x.id}>…{x.id.slice(-8)}</span>
        <span className="mono"> #{x.seq}</span>
      </p>
    </header>
  );
}

/** The action to create a rule (override), or the reason it cannot be done, and the result of the last creation. */
function DeriveBlock({
  x,
  routes,
  drafting,
  created,
  onDraft,
  onShowRoute,
}: {
  x: Exchange;
  routes: Load<RouteResource[]>;
  drafting: boolean;
  created: { route: string; name: string; enabled: boolean } | null;
  onDraft: () => void;
  onShowRoute: (name: string) => void;
}) {
  if (created) {
    return (
      <p className="xd__done" role="status">
        Rule <span className="mono">{created.name}</span> created in service <span className="mono">{created.route}</span>,{" "}
        {created.enabled ? "already in effect" : "switched off"}.{" "}
        <button type="button" className="link-button" onClick={() => onShowRoute(created.route)}>
          Adjust it in the service
        </button>
      </p>
    );
  }
  if (drafting) return null;
  const known = routes.kind === "ready" && x.route ? routes.data.some((r) => r.route.name === x.route) : true;
  const reason = !x.route
    ? "No service matched this exchange, and a rule always lives in a service."
    : !known
      ? `The service ${x.route} no longer exists.`
      : x.outcome === "synthesized"
        ? "This exchange's response was synthesized by a rule: there is no response from the destination to copy."
        : x.outcome === "dropped"
          ? "This exchange's connection was dropped: there is no response to copy."
          : x.outcome === "gateway"
            ? "This exchange ended in an error from the gateway itself: there is no response from the destination to copy."
            : null;
  return (
    <div className="xd__derive">
      <button type="button" className="button" onClick={onDraft} disabled={reason !== null} aria-describedby={reason ? "xd-derive-why" : undefined}>
        <PlusIcon /> Create a rule from this exchange
      </button>
      {reason ? (
        <span className="dim" id="xd-derive-why">
          {reason}
        </span>
      ) : (
        <span className="dim">Builds a draft with this request and this response; nothing is written until you confirm.</span>
      )}
    </div>
  );
}

// ---------- Time ----------

/**
 * A "round" step for the axis ticks: 1, 2 or 5 times a power of 10. It is the
 * smallest step that leaves at most `most` ticks beyond zero (the width of the
 * track decides how many fit without the labels touching each other).
 */
/** Short label for a round axis tick: "0", "200 ms", "1 s", "1.5 s". */
function tickLabel(v: number): string {
  if (v === 0) return "0";
  if (v >= 1000) {
    const s = v / 1000;
    return `${num(s, Number.isInteger(s) ? 0 : 1)} s`;
  }
  return `${num(v, Number.isInteger(v) ? 0 : 1)} ms`;
}

function tickStep(total: number, most: number): number {
  const limit = Math.max(1, most);
  const p = 10 ** Math.floor(Math.log10(total / limit));
  for (const m of [1, 2, 5, 10, 20]) if (Math.floor(total / (m * p) + 1e-9) <= limit) return m * p;
  return 20 * p;
}

/**
 * A waterfall at true scale: an axis from 0 to the total, and each quantity in
 * its own lane, starting where the previous one ends. The order is that of the
 * request's path: the upstream responds, the injected delay comes after the
 * response is ready, and the gateway's overhead (summed along the path) closes
 * the account.
 */
function TimeLanes({ x }: { x: Exchange }) {
  const t = x.timing;
  const total = Math.max(t.totalMs, t.upstreamMs + t.injectedMs + t.gatewayMs, 0.001);
  const pct = (v: number) => (v / total) * 100;
  // One tick every ~64px of the track (short labels: "1 s", "200 ms"): at
  // 390px only the zero and one tick are left, and the labels never touch.
  const track = useRef<HTMLTableCellElement>(null);
  const [trackWidth, setTrackWidth] = useState(0);
  useEffect(() => {
    const el = track.current;
    if (!el) return;
    const ro = new ResizeObserver(([entry]) => setTrackWidth(entry?.contentRect.width ?? 0));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const step = tickStep(total, trackWidth > 0 ? Math.min(5, Math.floor(trackWidth / 64)) : 2);
  const ticks: number[] = [];
  for (let i = 0; i * step <= total + 1e-9; i++) ticks.push(Number((i * step).toPrecision(12)));
  // Stuck to the end of the track, the last tick aligns its label to the right
  // so it does not spill past the edge.
  const endTick =
    ticks.length > 1 && trackWidth > 0 && (1 - ticks[ticks.length - 1]! / total) * trackWidth < 22
      ? ticks[ticks.length - 1]
      : undefined;
  const lanes = [
    {
      key: "upstream",
      label: "destination",
      value: t.upstreamMs,
      start: 0,
      empty:
        x.outcome === "synthesized"
          ? "not consulted"
          : x.outcome === "dropped"
            ? "connection dropped"
            : x.outcome === "gateway"
              ? "no response"
              : "0 ms",
    },
    { key: "injected", label: "injected", value: t.injectedMs, start: t.upstreamMs, empty: "no delay" },
    { key: "gateway", label: "gateway", value: t.gatewayMs, start: t.upstreamMs + t.injectedMs, empty: "0 ms" },
  ];
  const headId = useId();
  return (
    <section className="xd__section" aria-labelledby={headId}>
      <h3 className="section-title" id={headId}>
        time
        <span className="section-title__note">
          total <span className="mono">{ms(t.totalMs)}</span>
          {t.injectedMs > 0 ? (
            <>
              {" "}
              · <span className="tone-injected">{Math.round(pct(t.injectedMs))}% injected</span>
            </>
          ) : null}
        </span>
      </h3>
      <table className="lanes">
        <caption className="sr-only">
          Breakdown of the exchange's time, on a linear scale from 0 to {ms(total)}
        </caption>
        <tbody>
          {lanes.map((l) => (
            <tr key={l.key} className={l.value > 0 ? undefined : "is-zero"}>
              <th scope="row" className="lanes__label">
                {l.label}
              </th>
              <td className="lanes__track">
                <span className="lanes__rail">
                  {l.value > 0 ? (
                    <span
                      className={`lanes__seg fall__seg--${l.key}`}
                      style={{ left: `${pct(l.start)}%`, width: `${pct(l.value)}%` }}
                    />
                  ) : null}
                </span>
              </td>
              <td className={"lanes__value" + (l.value > 0 ? " mono" : " dim")}>
                {l.value > 0 ? (
                  <>
                    {ms(l.value)} <span className="dim">{Math.round(pct(l.value))}%</span>
                  </>
                ) : (
                  l.empty
                )}
              </td>
            </tr>
          ))}
          <tr className="lanes__axis" aria-hidden="true">
            <th />
            <td className="lanes__track" ref={track}>
              <span className="lanes__ticks">
                {ticks.map((v) => (
                  <span
                    key={v}
                    className={"lanes__tick" + (v === endTick ? " lanes__tick--end" : "")}
                    style={{ left: `${pct(v)}%` }}
                  >
                    {tickLabel(v)}
                  </span>
                ))}
              </span>
            </td>
            <td />
          </tr>
        </tbody>
      </table>
      {t.injectedMs > 0 ? (
        <p className="hint">
          The delay is applied after the response is ready, so it adds to the destination's time instead of overlapping
          it.
        </p>
      ) : null}
    </section>
  );
}

// ---------- Request and response ----------

function MessageSection({ title, m, x, kind }: { title: string; m: Message; x: Exchange; kind: "request" | "response" }) {
  const headId = useId();
  const decoded = decodeMessage(m);
  const headers = Object.entries(m.headers ?? {})
    .filter((e): e is [string, string[]] => Array.isArray(e[1]) && e[1].length > 0)
    .sort(([a], [b]) => a.localeCompare(b));
  const noResponse = kind === "response" && (x.outcome === "dropped" || (!x.status && headers.length === 0));
  return (
    <section className="xd__section" aria-labelledby={headId}>
      <h3 className="section-title" id={headId}>
        {title}
        <span className="section-title__note">
          {kind === "request" ? (
            <span className="mono">
              {x.host}
              {x.clientAddr ? ` · from ${x.clientAddr}` : ""}
            </span>
          ) : noResponse ? (
            "no response was written"
          ) : (
            <span className="mono">status {x.status}</span>
          )}
        </span>
      </h3>
      {noResponse ? null : (
        <>
          {headers.length ? (
            <dl className="hdrs">
              {headers.map(([k, vs]) => (
                <div className="hdrs__row" key={k}>
                  <dt className="mono">{k}</dt>
                  <dd className="mono">
                    {vs.map((v, i) => (
                      <span key={i} className="hdrs__val">
                        {v}
                      </span>
                    ))}
                  </dd>
                </div>
              ))}
            </dl>
          ) : (
            <p className="dim xd__pad-x">no headers</p>
          )}
          <BodyView d={decoded} />
        </>
      )}
    </section>
  );
}

function BodyView({ d }: { d: DecodedBody }) {
  const v = d.view;
  const kindLabel = v.kind === "json" ? (v.partial ? "incomplete JSON" : "JSON") : v.kind === "text" ? "text" : v.kind === "binary" ? "binary" : null;
  return (
    <div className="body">
      <p className="body__head">
        <span className="body__label">body</span>
        {v.kind === "empty" ? (
          <span className="dim">{d.size > 0 ? `${bytes(d.size)} not captured` : "empty"}</span>
        ) : (
          <span className="dim">
            {kindLabel}
            {d.contentType ? <span className="mono"> · {d.contentType}</span> : null}
            <span className="mono"> · {bytes(d.size)}</span>
          </span>
        )}
      </p>
      {d.truncated ? (
        <p className="body__cut" role="note">
          <strong>Body truncated on capture:</strong> kept <span className="mono">{bytes(d.captured, d.size)}</span> of{" "}
          <span className="mono">{bytes(d.size)}</span> (limit <span className="mono">capture.maxBodyBytes</span>). The
          end does not appear here and the JSON may not close.
        </p>
      ) : null}
      {v.kind === "json" || v.kind === "text" ? (
        <>
          <pre className={"code" + (d.truncated ? " code--cut" : "")} tabIndex={0} aria-label="Body content">
            {v.text}
          </pre>
          {d.truncated ? (
            <p className="body__tail">
              truncated here: <span className="mono">{bytes(d.size - d.captured)}</span> more were not captured
            </p>
          ) : null}
        </>
      ) : null}
      {v.kind === "binary" ? (
        <>
          <p className="dim xd__pad-x">
            Binary content: the first {v.shown} bytes in hexadecimal
            {d.captured > v.shown ? `, of ${bytes(d.captured)} captured` : ""}.
          </p>
          <pre className="code code--hex" tabIndex={0} aria-label="Start of the body in hexadecimal">
            {v.hex}
          </pre>
        </>
      ) : null}
    </div>
  );
}

function Routing({ x }: { x: Exchange }) {
  const headId = useId();
  const rows: [string, string | undefined][] = [
    ["rule", x.override],
    ["interventions", (x.interventions ?? []).length ? (x.interventions ?? []).join(", ") : "none"],
    ["outcome", x.outcome],
    ["drop", x.dropMode],
    ["start", x.start],
    ["id", x.id],
  ];
  return (
    <section className="xd__section" aria-labelledby={headId}>
      <h3 className="section-title" id={headId}>
        record
      </h3>
      <dl className="hdrs">
        {rows
          .filter(([, v]) => v)
          .map(([k, v]) => (
            <div className="hdrs__row" key={k}>
              <dt>{k}</dt>
              <dd className="mono">{v}</dd>
            </div>
          ))}
      </dl>
    </section>
  );
}
