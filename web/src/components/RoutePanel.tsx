import { useCallback, useEffect, useId, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import {
  api,
  type ExchangeFilter,
  type LearningView,
  type Route,
  type RouteResource,
  type SettingsPatchResult,
  type UpstreamHealth,
} from "../api";
import { clock, fieldProblem, PATH_HINT, rulePathProblem, shortPath } from "../format";
import type { OnOverrideState } from "../live";
import { useCommentsGuard, WriteCancelled, type CommentsGuard, type GuardedDoc } from "../commentsGuard";
import type { Load } from "../hooks";
import { toApiError, useNow } from "../hooks";
import { MODE_OPTIONS, type DetailMode } from "../mode";
import type { MergePatch } from "../patch";
import type { Selection } from "../selection";
import { suggest } from "../forward";
import { destinationText, knownDestinations } from "../services";
import { usePatchWriter } from "../writer";
import { DurationField, Row, Segmented, TextField } from "./Controls";
import { ForwardKeys, ForwardPreview } from "./ForwardPreview";
import { PathText } from "./PathText";
import { ErrorNote } from "./ErrorNote";
import { PlusIcon } from "./Icons";
import { OverrideItem } from "./OverrideItem";
import { ProcessBody } from "./ProcessPanel";
import { ExchangeDetail, type OpenedExchange } from "./ExchangeDetail";
import { Empty } from "./States";
import type { ApiError } from "../api";

export type ControlView = "route" | "process" | "exchange";

interface RoutePanelProps {
  view: ControlView;
  onView: (v: ControlView) => void;
  routes: Load<RouteResource[]>;
  selection: Selection;
  onSelect: (s: Selection) => void;
  onRetry: () => void;
  learning: Load<LearningView>;
  /** Opens an exchange in the detail (the origin of a learned or derived override). */
  onShowExchange: (id: string) => void;
  /** Changes on every settings event: the process view re-reads the settings. */
  version: number;
  /** Exchange opened in the detail (the "exchange" tab), or null. */
  exchange: OpenedExchange | null;
  onOpenExchange: (ex: OpenedExchange) => void;
  onCloseExchange: () => void;
  /** Active traffic filter, which the item-by-item navigation respects. */
  exchangeFilter: ExchangeFilter;
  /** Upstream health, shown when an upstream is selected. */
  upstreams: Load<UpstreamHealth[]>;
  onSettings: (r: SettingsPatchResult) => void;
  onOverrideState: OnOverrideState;
  /** Identifies the project in the remembered comment confirmations (routes directory). */
  guardScope: string;
  /** New-service form open: it takes over the service tab even with one selected. */
  creating: boolean;
  onCreate: () => void;
  onCreated: (name: string) => void;
  onCancelCreate: () => void;
  /** Simple (the default) or advanced, remembered between visits. */
  mode: DetailMode;
  onMode: (m: DetailMode) => void;
  /** The process's traffic port: the address the app calls in the forwarding preview. */
  trafficPort: number | undefined;
}

/**
 * Detail panel, in the right column: the service selected in the services panel
 * (the route's rules and fields) or the form for a new one, the process
 * (gateway.json) or the exchange opened from the traffic.
 */
export function RoutePanel(props: RoutePanelProps) {
  const { view, onView, routes, selection, exchange } = props;
  const guard = useCommentsGuard(props.guardScope);
  const selected = selection?.kind === "route" ? selection.name : null;
  const res = routes.kind === "ready" && selected ? routes.data.find((r) => r.route.name === selected) : undefined;
  const tabsId = useId();
  const creating = props.creating && view === "route";
  // Newly created service: until the list re-read brings it in, it is arriving, not gone.
  const [justCreated, setJustCreated] = useState<string | null>(null);
  const onCreated = props.onCreated;
  const created = useCallback(
    (name: string) => {
      setJustCreated(name);
      onCreated(name);
    },
    [onCreated],
  );

  return (
    <section className="panel" aria-label="Detail">
      <header className="panel__head">
        <Tabs id={tabsId} view={view} onView={onView} hasExchange={exchange !== null} />
        {creating ? <span className="panel__sub">new service</span> : null}
        {view === "route" && !creating && res ? <span className="panel__sub">{res.route.name}</span> : null}
        {view === "process" ? <span className="panel__sub mono">gateway.json</span> : null}
        {view === "exchange" && exchange ? (
          <span className="panel__sub mono" title={exchange.id}>
            …{exchange.id.slice(-8)}
          </span>
        ) : null}
        <div className="panel__tools">
          <Segmented<DetailMode>
            label="Detail mode"
            value={props.mode}
            onChange={props.onMode}
            options={MODE_OPTIONS}
          />
        </div>
      </header>
      <div className="panel__body" id={`${tabsId}-panel`} role="tabpanel" aria-labelledby={`${tabsId}-${view}`}>
        {guard.asking ? <CommentsPrompt guard={guard} /> : null}
        {view === "process" ? (
          <ProcessBody
            version={props.version}
            onSettings={props.onSettings}
            mode={props.mode}
            onAdvanced={() => props.onMode("advanced")}
          />
        ) : creating ? (
          <NewServiceForm
            routes={routes}
            upstreams={props.upstreams}
            trafficPort={props.trafficPort}
            onCreated={created}
            onCancel={props.onCancelCreate}
          />
        ) : view === "exchange" && exchange ? (
          <ExchangeDetail
            opened={exchange}
            filter={props.exchangeFilter}
            onOpen={props.onOpenExchange}
            onClose={props.onCloseExchange}
            routes={routes}
            guard={guard}
            onShowRoute={(name) => props.onSelect({ kind: "route", name })}
          />
        ) : (
          <RouteBody {...props} guard={guard} res={res} justCreated={justCreated} />
        )}
      </div>
    </section>
  );
}

function Tabs({
  id,
  view,
  onView,
  hasExchange,
}: {
  id: string;
  view: ControlView;
  onView: (v: ControlView) => void;
  hasExchange: boolean;
}) {
  const refs = useRef<Record<ControlView, HTMLButtonElement | null>>({ route: null, process: null, exchange: null });
  const tabs: { v: ControlView; label: string }[] = [
    { v: "route", label: "service" },
    { v: "process", label: "process" },
    ...(hasExchange ? [{ v: "exchange" as const, label: "exchange" }] : []),
  ];
  const onKey = (e: KeyboardEvent) => {
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
    e.preventDefault();
    const i = tabs.findIndex((t) => t.v === view);
    const next = tabs[(i + (e.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length]!.v;
    onView(next);
    refs.current[next]?.focus();
  };
  return (
    <div className="tabs" role="tablist" aria-label="Detail">
      {tabs.map((t) => (
        <button
          key={t.v}
          ref={(el) => {
            refs.current[t.v] = el;
          }}
          id={`${id}-${t.v}`}
          type="button"
          role="tab"
          aria-selected={view === t.v}
          aria-controls={`${id}-panel`}
          tabIndex={view === t.v ? 0 : -1}
          className={"tab" + (view === t.v ? " tab--on" : "")}
          onClick={() => onView(t.v)}
          onKeyDown={onKey}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}

/** The one-time confirmation before rewriting a document that has comments. */
function CommentsPrompt({ guard }: { guard: CommentsGuard }) {
  const titleId = useId();
  const confirmRef = useRef<HTMLButtonElement>(null);
  const returnTo = useRef<Element | null>(null);
  useEffect(() => {
    returnTo.current = document.activeElement;
    confirmRef.current?.focus();
    return () => {
      const el = returnTo.current;
      if (el instanceof HTMLElement && el.isConnected) el.focus();
    };
  }, [guard.asking]);
  return (
    <div
      className="guard"
      role="alertdialog"
      aria-labelledby={titleId}
      aria-describedby={`${titleId}-d`}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          guard.cancel();
        }
      }}
    >
      <p className="guard__title" id={titleId}>
        <span className="mono" title={guard.asking ?? undefined}>{shortPath(guard.asking ?? "")}</span> has comments
      </p>
      <p id={`${titleId}-d`}>
        Saving from the panel rewrites the whole file: the comments and the order of the keys are lost. This question
        appears once per file. To keep the comments, edit the file in your editor and reload.
      </p>
      <p className="guard__actions">
        <button ref={confirmRef} type="button" className="button button--primary" onClick={guard.confirm}>
          Save and lose the comments
        </button>
        <button type="button" className="button" onClick={guard.cancel}>
          Cancel the change
        </button>
      </p>
    </div>
  );
}

function RouteBody({
  routes,
  selection,
  onSelect,
  onRetry,
  learning,
  onShowExchange,
  onOverrideState,
  upstreams,
  onCreate,
  guard,
  res,
  justCreated,
  mode,
  onMode,
  trafficPort,
}: RoutePanelProps & { guard: CommentsGuard; res: RouteResource | undefined; justCreated: string | null }) {
  if (routes.kind === "loading") return <Empty title="Loading the services" />;
  if (routes.kind === "error") {
    return (
      <Empty title="Services unavailable">
        <p>
          <span className="mono">GET /api/routes</span> failed ({routes.error.code}). The detail is in the services
          panel.{" "}
          <button type="button" className="link-button" onClick={onRetry}>
            Try again
          </button>
        </p>
      </Empty>
    );
  }
  if (selection?.kind === "upstream") {
    const users = routes.data.filter((r) => r.route.upstream === selection.name);
    const health = upstreams.kind === "ready" ? upstreams.data.find((u) => u.upstream === selection.name) : undefined;
    return (
      <Empty title="Filter by destination">
        <UpstreamHealthBlock health={health} load={upstreams} />
        <p>
          The traffic shows only what went to <span className="mono">{selection.name}</span>. The rules live in the
          services; pick one of the ones that use this destination:
        </p>
        <p className="inline inline--wrap">
          {users.map((r) => (
            <button
              key={r.route.name}
              type="button"
              className="text-button mono"
              onClick={() => onSelect({ kind: "route", name: r.route.name })}
            >
              {r.route.name}
            </button>
          ))}
        </p>
      </Empty>
    );
  }
  if (selection?.kind !== "route") {
    return (
      <div className="state">
        <p className="state__title">No service selected</p>
        <p>Select a service on the map or in the list to adjust its rules, or create a new one.</p>
        <p>
          <button type="button" className="text-button" onClick={onCreate}>
            <PlusIcon /> service
          </button>
        </p>
      </div>
    );
  }
  if (!res && justCreated === selection.name) return <Empty title={`Opening the service ${selection.name}`} />;
  if (!res) {
    return (
      <Empty title={`The service ${selection.name} no longer exists`}>
        <p>It was removed or renamed since it was selected.</p>
      </Empty>
    );
  }
  const learned = learning.kind === "ready" ? (learning.data.learned[res.route.name] ?? 0) : 0;
  return (
    <RouteDetail
      key={res.route.name}
      res={res}
      guard={guard}
      learned={learned}
      onShowExchange={onShowExchange}
      onOverrideState={onOverrideState}
      onRenamed={(name) => onSelect({ kind: "route", name })}
      onDeleted={() => onSelect(null)}
      mode={mode}
      onAdvanced={() => onMode("advanced")}
      trafficPort={trafficPort}
    />
  );
}

function RouteDetail({
  res,
  guard,
  learned,
  onShowExchange,
  onOverrideState,
  onRenamed,
  onDeleted,
  mode,
  onAdvanced,
  trafficPort,
}: {
  res: RouteResource;
  guard: CommentsGuard;
  learned: number;
  onShowExchange: (id: string) => void;
  onOverrideState: OnOverrideState;
  onRenamed: (name: string) => void;
  onDeleted: () => void;
  mode: DetailMode;
  onAdvanced: () => void;
  trafficPort: number | undefined;
}) {
  const r = res.route;
  const overrides = r.overrides ?? [];
  const doc: GuardedDoc = useMemo(() => ({ file: res.file, hasComments: res.hasComments }), [res.file, res.hasComments]);
  // Document rewritten without comments: the old confirmation no longer holds.
  useEffect(() => guard.observe(doc), [guard, doc]);
  // The instant this live state arrived: the base for the TTL countdown.
  const at = useMemo(() => Date.now(), [res]);
  const ticking = overrides.some((o) => res.state[o.name]?.ttlRemainingMs != null);
  const now = useNow(ticking);
  const [adding, setAdding] = useState(false);
  const simple = mode === "simple";

  return (
    <div className="route">
      {simple ? (
        <>
          <RouteSummary res={res} onAdvanced={onAdvanced} />
          <div className="route__preview">
            <ForwardPreview
              input={{
                path: r.match.path ?? "",
                host: r.match.host ?? "",
                destination: r.upstream ?? "",
                stripPrefix: r.stripPrefix === true,
                rewriteHost: r.rewriteHost === true,
                trafficPort,
              }}
            />
          </div>
        </>
      ) : null}
      <div className="route__section">
        <h3 className="section-title">
          rules
          {overrides.length ? <span className="section-title__count mono">{overrides.length}</span> : null}
          {learned ? (
            <span className="section-title__note">
              {learned} {learned === 1 ? "learned rule" : "learned rules"}
            </span>
          ) : null}
        </h3>
        {overrides.length === 0 ? (
          <p className="dim">
            {r.upstream
              ? "No rules: the gateway forwards everything to the destination."
              : "No rules and no destination: the gateway answers 501 to this service's requests."}
          </p>
        ) : (
          <ul className="ovs">
            {overrides.map((o) => (
              <OverrideItem
                key={o.name}
                route={r.name}
                doc={doc}
                override={o}
                state={res.state[o.name]}
                at={at}
                now={now}
                guard={guard}
                onShowExchange={onShowExchange}
                onState={onOverrideState}
                mode={mode}
                onAdvanced={onAdvanced}
              />
            ))}
          </ul>
        )}
        {learned > 1 && !simple ? (
          <p className="hint">
            Learning already swaps identifiers for <span className="mono path-param">:id</span>. To cover other values
            at once (a slug, for example), use <span className="mono path-param">:id</span> on the segment that varies,
            or a suffix wildcard such as <span className="mono">/users/*</span>, and remove the learned ones.
          </p>
        ) : null}
        {adding ? (
          <NewOverrideForm route={r} doc={doc} guard={guard} onDone={() => setAdding(false)} />
        ) : (
          <button type="button" className="text-button" onClick={() => setAdding(true)}>
            <PlusIcon /> rule
          </button>
        )}
      </div>

      {simple ? null : (
        <RouteFields
          res={res}
          doc={doc}
          guard={guard}
          onRenamed={onRenamed}
          onDeleted={onDeleted}
          trafficPort={trafficPort}
        />
      )}
    </div>
  );
}

/**
 * The service on one line in simple mode: name, entry and destination
 * ("payments · /payments/* → 127.0.0.1:9001"), with what the forwarding changes
 * in tertiary ink. Editing these fields is a gesture of the advanced mode.
 */
function RouteSummary({ res, onAdvanced }: { res: RouteResource; onAdvanced: () => void }) {
  const r = res.route;
  const extras: string[] = [];
  if (r.stripPrefix) extras.push("only what comes after the prefix");
  if (r.rewriteHost) extras.push("Host of the destination");
  if (r.timeout) extras.push(`timeout ${r.timeout}`);
  return (
    <p className="route__summary">
      <span className="mono route__summary-name">{r.name}</span>
      <span className="dim" aria-hidden="true"> · </span>
      <span className="mono">
        {r.match.host ? <span className="route__summary-host">{r.match.host}</span> : null}
        {r.match.path ? <PathText path={r.match.path} /> : <span className="dim">any entry</span>}
      </span>
      <span className="dim" aria-hidden="true"> → </span>
      {r.upstream ? (
        <span className="mono" title={r.upstream}>
          {destinationText(r.upstream)}
        </span>
      ) : (
        <span className="dim">no destination</span>
      )}
      {extras.length ? <span className="dim">{` · ${extras.join(" · ")}`}</span> : null}
      <button type="button" className="text-button route__summary-edit" onClick={onAdvanced}>
        edit the service
      </button>
    </p>
  );
}

/** Health of the selected destination: what, on the list row, only fits in the tooltip. */
function UpstreamHealthBlock({ health, load }: { health: UpstreamHealth | undefined; load: Load<UpstreamHealth[]> }) {
  if (load.kind === "loading") return <p className="dim">Reading the state of the destination.</p>;
  if (load.kind === "error") {
    return (
      <p className="dim">
        Health unknown: <span className="mono">GET /api/upstreams</span> answered{" "}
        <span className="mono">{load.error.code}</span>.
      </p>
    );
  }
  if (!health) return <p className="dim">No request forwarded to this destination yet.</p>;
  const state =
    health.status === "down" ? "down" : health.status === "up" ? "up, responding" : "no recent attempts";
  return (
    <dl className="uph">
      <dt>state</dt>
      <dd className={health.status === "down" ? "is-fault" : undefined}>{state}</dd>
      <dt>recent</dt>
      <dd className="mono">
        {health.recent.failures} {health.recent.failures === 1 ? "failure" : "failures"} in {health.recent.attempts}{" "}
        {health.recent.attempts === 1 ? "attempt" : "attempts"}
      </dd>
      {health.lastError ? (
        <>
          <dt>last error</dt>
          <dd className="mono">{health.lastError}</dd>
        </>
      ) : null}
      {health.lastFailureAt ? (
        <>
          <dt>last failure</dt>
          <dd className="mono">{clock(health.lastFailureAt)}</dd>
        </>
      ) : null}
      {health.lastSuccessAt ? (
        <>
          <dt>last success</dt>
          <dd className="mono">{clock(health.lastSuccessAt)}</dd>
        </>
      ) : null}
    </dl>
  );
}

type RouteFieldsValue = Omit<Route, "overrides">;

function strip(r: Route): RouteFieldsValue {
  const { overrides: _o, ...rest } = r;
  return rest;
}

/** The route's own fields, saved by PATCH when leaving each field. */
function RouteFields({
  res,
  doc,
  guard,
  onRenamed,
  onDeleted,
  trafficPort,
}: {
  res: RouteResource;
  doc: GuardedDoc;
  guard: CommentsGuard;
  onRenamed: (name: string) => void;
  onDeleted: () => void;
  trafficPort: number | undefined;
}) {
  const name = res.route.name;
  const base = useMemo(() => strip(res.route), [res.route]);
  const send = useCallback(
    (patch: MergePatch) => guard.guard(doc, () => api.patchRoute(name, patch)).then((x) => strip(x.route)),
    [guard, doc, name],
  );
  const w = usePatchWriter<RouteFieldsValue>(base, send);
  const v = w.value;
  const ids = useId();
  const [actionError, setActionError] = useState<ApiError | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const act = (run: () => Promise<unknown>, then?: () => void) => {
    setActionError(null);
    run().then(then, (e: unknown) => {
      if (!(e instanceof WriteCancelled)) setActionError(toApiError(e));
    });
  };

  const text = (key: "upstream", t: string) => w.change({ [key]: t.trim() || null });

  // The preview follows the typing, not the saving: what is in the field now
  // wins over what was saved, until the saved value changes.
  const [typing, setTyping] = useState<{ host?: string; path?: string; upstream?: string }>({});
  useEffect(() => setTyping({}), [v.match.host, v.match.path, v.upstream]);
  const preview = {
    path: typing.path ?? v.match.path ?? "",
    host: typing.host ?? v.match.host ?? "",
    destination: typing.upstream ?? v.upstream ?? "",
    stripPrefix: v.stripPrefix === true,
    rewriteHost: v.rewriteHost === true,
    trafficPort,
  };
  const pathExact = preview.path.trim() !== "" && !preview.path.trim().endsWith("/*");

  return (
    <div className="route__section">
      <h3 className="section-title">
        service
        <span className="section-title__note">
          #{res.order + 1} in precedence · <span className="mono" title={res.file}>{shortPath(res.file)}</span>
          {w.busy ? " · saving" : ""}
        </span>
      </h3>
      <div className="form">
        <Row label="name" htmlFor={`${ids}-name`} hint="renames the service; the file stays the same">
          <TextField
            id={`${ids}-name`}
            label="Service name"
            mono
            size="sm"
            value={v.name}
            validate={(t) => (t.trim() ? null : "the name is required")}
            onCommit={(t) =>
              act(
                () => guard.guard(doc, () => api.replaceRoute(name, { ...res.route, name: t.trim() })),
                () => onRenamed(t.trim()),
              )
            }
          />
        </Row>
        <h4 className="form__group">entry</h4>
        <Row label="host" htmlFor={`${ids}-host`} hint="the Host your app calls; empty accepts any">
          <TextField
            id={`${ids}-host`}
            label="Entry Host"
            mono
            size="sm"
            value={v.match.host ?? ""}
            placeholder="any"
            onDraft={(t) => setTyping((d) => ({ ...d, host: t }))}
            onCommit={(t) => w.change({ match: { host: t.trim() || null } })}
          />
        </Row>
        <Row
          label="path"
          htmlFor={`${ids}-path`}
          hint={
            pathExact ? (
              <>
                only this path; use <span className="mono">{preview.path.trim().replace(/\/+$/, "")}/*</span> for
                everything below
              </>
            ) : (
              "exact, or a suffix wildcard: /api/x/*"
            )
          }
        >
          <TextField
            id={`${ids}-path`}
            label="Entry path"
            mono
            size="sm"
            value={v.match.path ?? ""}
            placeholder="any"
            onDraft={(t) => setTyping((d) => ({ ...d, path: t }))}
            onCommit={(t) => w.change({ match: { path: t.trim() || null } })}
          />
        </Row>
        <h4 className="form__group">destination</h4>
        <Row
          label="url"
          htmlFor={`${ids}-up`}
          hint={v.upstream ? "where the gateway forwards to" : "no destination, only the rules answer"}
        >
          <TextField
            id={`${ids}-up`}
            label="Destination"
            mono
            size="sm"
            value={v.upstream ?? ""}
            placeholder="http://localhost:9001"
            onDraft={(t) => setTyping((d) => ({ ...d, upstream: t }))}
            validate={(t) => (t.trim() === "" || /^https?:\/\/.+/.test(t.trim()) ? null : "an http:// or https:// URL")}
            onCommit={(t) => text("upstream", t)}
          />
        </Row>
        <ForwardKeys
          input={preview}
          onStripPrefix={(b) => w.change({ stripPrefix: b ? true : null })}
          onRewriteHost={(b) => w.change({ rewriteHost: b ? true : null })}
        />
        <Row label="timeout" htmlFor={`${ids}-to`} hint="empty: no limit of its own">
          <DurationField
            id={`${ids}-to`}
            label="Timeout"
            value={v.timeout}
            placeholder="no limit"
            onCommit={(d) => w.change({ timeout: d })}
          />
        </Row>
        <h4 className="form__group">preview</h4>
        <ForwardPreview input={preview} />
      </div>
      {w.error ? <ErrorNote error={w.error} onDismiss={w.dismissError} what="The change was not saved" /> : null}
      {actionError ? <ErrorNote error={actionError} onDismiss={() => setActionError(null)} what="The action failed" /> : null}
      <p className="route__danger">
        {confirmDelete ? (
          <span className="inline">
            <button
              type="button"
              className="text-button text-button--danger"
              autoFocus
              onClick={() => act(() => api.deleteRoute(name), onDeleted)}
            >
              delete {shortPath(res.file)} and the service {name}
            </button>
            <button type="button" className="text-button" onClick={() => setConfirmDelete(false)}>
              keep
            </button>
          </span>
        ) : (
          <button type="button" className="text-button" onClick={() => setConfirmDelete(true)}>
            remove service
          </button>
        )}
      </p>
    </div>
  );
}

/** A new rule (override): it starts off, and the first continuous adjustment turns it on. */
function NewOverrideForm({
  route,
  doc,
  guard,
  onDone,
}: {
  route: Route;
  doc: GuardedDoc;
  guard: CommentsGuard;
  onDone: () => void;
}) {
  const ids = useId();
  const [name, setName] = useState("");
  const [path, setPath] = useState(route.match.path ?? "");
  const [method, setMethod] = useState("");
  const [status, setStatus] = useState("503");
  const [error, setError] = useState<ApiError | null>(null);
  const [sending, setSending] = useState(false);
  // The path problem appears when leaving the field or when trying to create,
  // not on every keystroke: "/zip/:" is an invalid path only until the
  // parameter is named.
  const [pathTouched, setPathTouched] = useState(false);
  const localPathProblem = rulePathProblem(path);
  const serverPathProblem = error ? fieldProblem(error.body, "match.path") : null;
  const pathProblem = (pathTouched ? localPathProblem : null) ?? serverPathProblem;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (localPathProblem) {
      setPathTouched(true);
      return;
    }
    setSending(true);
    setError(null);
    guard
      .guard(doc, () =>
        api.createOverride(route.name, {
          name: name.trim(),
          enabled: false,
          match: { ...(path.trim() ? { path: path.trim() } : {}), ...(method.trim() ? { method: method.trim().toUpperCase() } : {}) },
          respond: { status: Number(status) || 503 },
        }),
      )
      .then(onDone, (err: unknown) => {
        setSending(false);
        if (!(err instanceof WriteCancelled)) setError(toApiError(err));
      });
  };

  return (
    <form className="create" onSubmit={submit} aria-labelledby={`${ids}-t`}>
      <p className="create__title" id={`${ids}-t`}>
        New rule in <span className="mono">{route.name}</span>
      </p>
      <div className="form">
        <Row label="name" htmlFor={`${ids}-n`}>
          <input id={`${ids}-n`} className="input input--mono input--sm" required autoFocus value={name} onChange={(e) => setName(e.target.value)} spellCheck={false} />
        </Row>
        <Row
          label="path"
          htmlFor={`${ids}-p`}
          hint={
            pathProblem ? (
              <span className="field__problem" id={`${ids}-pe`}>
                {pathProblem}
              </span>
            ) : (
              PATH_HINT
            )
          }
        >
          <input
            id={`${ids}-p`}
            className={"input input--mono input--sm" + (pathProblem ? " input--bad" : "")}
            value={path}
            placeholder="any"
            aria-invalid={pathProblem ? true : undefined}
            aria-describedby={pathProblem ? `${ids}-pe` : undefined}
            onChange={(e) => {
              setPath(e.target.value);
              if (serverPathProblem) setError(null);
            }}
            onBlur={() => setPathTouched(true)}
            spellCheck={false}
          />
        </Row>
        <Row label="method" htmlFor={`${ids}-m`}>
          <input id={`${ids}-m`} className="input input--mono input--sm" list="http-methods" value={method} placeholder="any" onChange={(e) => setMethod(e.target.value)} spellCheck={false} />
        </Row>
        <Row label="answers" htmlFor={`${ids}-s`}>
          <input id={`${ids}-s`} className="input input--mono input--sm" inputMode="numeric" pattern="[1-5][0-9]{2}" value={status} onChange={(e) => setStatus(e.target.value)} />
        </Row>
      </div>
      <p className="hint">It starts off. Adjusting a frequency, the latency or the drop turns it on.</p>
      {error && !serverPathProblem ? <ErrorNote error={error} what="The rule was not created" /> : null}
      <p className="inline">
        <button type="submit" className="button button--primary" disabled={sending || !name.trim()}>
          Create rule
        </button>
        <button type="button" className="button" onClick={onDone}>
          Cancel
        </button>
      </p>
    </form>
  );
}

/** How many known destinations become one-click shortcuts in the new-service form; the rest stay in the field's suggestions. */
const QUICK_DESTINATIONS = 6;

/**
 * New-service form: name, entry and destination. It opens from the list's
 * "+ service" with or without a service selected; the rules come later, in the
 * service itself.
 */
function NewServiceForm({
  routes,
  upstreams,
  trafficPort,
  onCreated,
  onCancel,
}: {
  routes: Load<RouteResource[]>;
  upstreams: Load<UpstreamHealth[]>;
  trafficPort: number | undefined;
  onCreated: (name: string) => void;
  onCancel: () => void;
}) {
  const ids = useId();
  const [name, setName] = useState("");
  const [path, setPath] = useState("");
  const [host, setHost] = useState("");
  const [showHost, setShowHost] = useState(false);
  const [dest, setDest] = useState("");
  // The two forwarding keys: while nobody touches them, they follow what the
  // destination asks for (suggest); touching one freezes both at what is in
  // view, so the suggestion does not pull back what was chosen.
  const [keys, setKeys] = useState<{ stripPrefix: boolean; rewriteHost: boolean } | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [sending, setSending] = useState(false);
  const known = useMemo(
    () =>
      knownDestinations(
        routes.kind === "ready" ? routes.data : [],
        upstreams.kind === "ready" ? upstreams.data : [],
      ),
    [routes, upstreams],
  );

  const n = name.trim();
  const taken = routes.kind === "ready" && routes.data.some((r) => r.route.name === n);
  const d = dest.trim();
  const destBad = d !== "" && !/^https?:\/\/.+/.test(d);
  const suggested = suggest(d);
  const chosen = keys ?? { stripPrefix: suggested?.stripPrefix ?? false, rewriteHost: suggested?.rewriteHost ?? false };
  const preview = { path, host, destination: d, ...chosen, trafficPort };
  const p = path.trim();
  const pathExact = p !== "" && !p.endsWith("/*");

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!n || taken || destBad) return;
    setSending(true);
    setError(null);
    api
      .createRoute({
        name: n,
        match: { ...(host.trim() ? { host: host.trim() } : {}), ...(path.trim() ? { path: path.trim() } : {}) },
        ...(d ? { upstream: d } : {}),
        ...(chosen.stripPrefix ? { stripPrefix: true } : {}),
        ...(chosen.rewriteHost ? { rewriteHost: true } : {}),
      })
      .then(
        () => onCreated(n),
        (err: unknown) => {
          setSending(false);
          setError(toApiError(err));
        },
      );
  };

  return (
    <div className="newsvc">
      <form
        className="create"
        onSubmit={submit}
        aria-labelledby={`${ids}-t`}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.preventDefault();
            onCancel();
          }
        }}
      >
        <p className="create__title" id={`${ids}-t`}>
          New service
        </p>
        <div className="form">
          <Row
            label="name"
            htmlFor={`${ids}-n`}
            hint={taken ? <span className="field__problem">a service with this name already exists</span> : undefined}
          >
            <input
              id={`${ids}-n`}
              className={"input input--mono input--sm" + (taken ? " input--bad" : "")}
              required
              autoFocus
              value={name}
              placeholder="orders"
              aria-invalid={taken || undefined}
              onChange={(e) => setName(e.target.value)}
              spellCheck={false}
            />
          </Row>
          <h4 className="form__group">entry</h4>
          <Row
            label="path"
            htmlFor={`${ids}-p`}
            hint={
              pathExact ? (
                <>
                  only this path; use <span className="mono">{p.replace(/\/+$/, "")}/*</span> for everything below
                </>
              ) : (
                "the path your app calls on the gateway; exact or a suffix wildcard"
              )
            }
          >
            <input
              id={`${ids}-p`}
              className="input input--mono input--sm"
              value={path}
              placeholder="/orders/*"
              onChange={(e) => setPath(e.target.value)}
              spellCheck={false}
            />
          </Row>
          {showHost ? (
            <Row label="host" htmlFor={`${ids}-h`} hint="empty accepts any Host">
              <input
                id={`${ids}-h`}
                className="input input--mono input--sm"
                autoFocus
                value={host}
                placeholder="orders.local"
                onChange={(e) => setHost(e.target.value)}
                spellCheck={false}
              />
            </Row>
          ) : (
            <Row label="">
              <button type="button" className="text-button" onClick={() => setShowHost(true)}>
                <PlusIcon /> host (optional)
              </button>
            </Row>
          )}
          <h4 className="form__group">destination</h4>
          <Row
            label="url"
            htmlFor={`${ids}-u`}
            hint={
              destBad ? (
                <span className="field__problem">use an http:// or https:// URL</span>
              ) : (
                "where the gateway forwards to; empty, only the rules answer"
              )
            }
          >
            <input
              id={`${ids}-u`}
              className={"input input--mono input--sm" + (destBad ? " input--bad" : "")}
              list={`${ids}-dests`}
              value={dest}
              placeholder={known[0] ?? "http://localhost:9004"}
              aria-invalid={destBad || undefined}
              onChange={(e) => setDest(e.target.value)}
              spellCheck={false}
            />
            <datalist id={`${ids}-dests`}>
              {known.map((u) => (
                <option key={u} value={u} />
              ))}
            </datalist>
          </Row>
          {known.length ? (
            <Row label="known">
              <span className="inline inline--wrap newsvc__known">
                {known.slice(0, QUICK_DESTINATIONS).map((u) => (
                  <button
                    key={u}
                    type="button"
                    className={"text-button mono" + (d === u ? " is-on" : "")}
                    aria-pressed={d === u}
                    title={`Use ${u} as the destination`}
                    onClick={() => setDest(u)}
                  >
                    {destinationText(u)}
                  </button>
                ))}
                {known.length > QUICK_DESTINATIONS ? (
                  <span className="dim">+{known.length - QUICK_DESTINATIONS} in the field's suggestions</span>
                ) : null}
              </span>
            </Row>
          ) : null}
          <ForwardKeys
            input={preview}
            why={keys ? null : suggested?.why}
            onStripPrefix={(b) => setKeys({ ...chosen, stripPrefix: b })}
            onRewriteHost={(b) => setKeys({ ...chosen, rewriteHost: b })}
          />
          <h4 className="form__group">preview</h4>
          <ForwardPreview input={preview} />
        </div>
        <p className="hint">
          Saved to <span className="mono">routes/{n || "name"}.yaml</span>. The chaos rules come later, in the service
          itself.
        </p>
        {error ? <ErrorNote error={error} what="The service was not created" /> : null}
        <p className="inline">
          <button type="submit" className="button button--primary" disabled={sending || !n || taken || destBad}>
            {sending ? "Creating…" : "Create service"}
          </button>
          <button type="button" className="button" onClick={onCancel}>
            Cancel
          </button>
        </p>
      </form>
    </div>
  );
}
