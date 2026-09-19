import { useCallback, useId, useState, type ReactNode } from "react";
import { api, type Latency, type Override, type OverrideLiveState, type OverrideResource } from "../api";
import { useStale, type OnOverrideState } from "../live";
import type { CommentsGuard, GuardedDoc } from "../commentsGuard";
import { WriteCancelled } from "../commentsGuard";
import { clock, formatDuration, latency as latencyText, ms, parseDuration, percent, PATH_HINT, rulePathProblem, seconds } from "../format";
import {
  chancePatch,
  declaredEffects,
  dropDeclared,
  latencyPatch,
  latencySpec,
  migrateProbability,
  neverApplies,
  type Effect as DeclaredEffect,
  type EffectKind,
} from "../effects";
import { RuleSelector } from "./PathText";
import { toApiError, useResource } from "../hooks";
import { composePatches, mergeDiff, type MergePatch } from "../patch";
import { advancedFeatures, advancedNote, type DetailMode } from "../mode";
import { isActive } from "../services";
import { usePatchWriter } from "../writer";
import {
  DurationField,
  DurationSlider,
  FrequencyControl,
  IntField,
  Row,
  Segmented,
  Switch,
  TextField,
} from "./Controls";
import { ChevronIcon, SlidersIcon } from "./Icons";
import { BodyMatcherEditor, MapEditor } from "./MapEditor";
import { ErrorNote } from "./ErrorNote";
import type { ApiError } from "../api";

interface OverrideItemProps {
  route: string;
  doc: GuardedDoc;
  override: Override;
  state: OverrideLiveState | undefined;
  /** Instant when `state` arrived, the base of the countdown. */
  at: number;
  now: number;
  guard: CommentsGuard;
  /** Highlights the exchange in the traffic list (it shows it when it is loaded). */
  onShowExchange: (id: string) => void;
  /** Live state returned by the write, applied straight away in the panel and in the map. */
  onState?: OnOverrideState;
  /** Simple shows the on/off switch, the name, the effects and their frequency; advanced, everything. */
  mode: DetailMode;
  /** Leads to the advanced mode from the advanced-feature mark. */
  onAdvanced: () => void;
}

/** The keys of the effects: touching one of them on a rule that is off also turns it on. */
const CONTINUOUS = ["respond", "latency", "drop"];

type LatMode = "none" | "fixed" | "range";

function latMode(l: Latency | undefined): LatMode {
  const s = latencySpec(l);
  if (!s) return "none";
  return s.min !== undefined || s.max !== undefined ? "range" : "fixed";
}

/** The name of an effect in a sentence: "response frequency of flaky". */
const EFFECT_NAME: Record<EffectKind, string> = {
  respond: "response",
  latency: "delay",
  drop: "drop",
};

/** One rule of the service: on/off, the frequency of each effect and the full edit. */
export function OverrideItem({
  route,
  doc,
  override,
  state,
  at,
  now,
  guard,
  onShowExchange,
  onState,
  mode,
  onAdvanced,
}: OverrideItemProps) {
  const simple = mode === "simple";
  const stale = useStale();
  const applyState = useCallback(
    (r: OverrideResource) => {
      if (r.state) onState?.(route, r.override.name, r.override.enabled !== false, r.state);
      return r;
    },
    [onState, route],
  );
  const send = useCallback(
    (patch: MergePatch) =>
      guard
        .guard(doc, () => api.patchOverride(route, override.name, patch))
        .then(applyState)
        .then((r) => r.override),
    [guard, doc, route, override.name, applyState],
  );
  const reset = () => act(() => api.resetOverride(route, o.name).then(applyState));
  const w = usePatchWriter<Override>(override, send);
  const o = w.value;
  const enabled = o.enabled !== false;
  const never = neverApplies(o);
  const active = isActive(o, state, now, at);
  const ids = useId();

  /**
   * Every write also migrates the legacy `probability`: each effect without a
   * frequency of its own keeps the one it had, and the field leaves the
   * document. On an already migrated rule this adds nothing.
   */
  const migrate = (patch: MergePatch) => composePatches(migrateProbability(o), patch);
  const change = (patch: MergePatch) => w.change(migrate(patch));

  /**
   * Adjusting an effect: on a rule that is off, the same gesture turns it on
   * (the API does not turn it on by itself; see PATCH in docs/api.md). What
   * counts is the effect the gesture touched, not what the migration added.
   */
  const adjust = (patch: MergePatch, delay?: number) => {
    const touches = Object.keys(patch).some((k) => CONTINUOUS.includes(k));
    const full = migrate(patch);
    w.change(touches && !enabled ? { ...full, enabled: true } : full, delay);
  };

  const [actionError, setActionError] = useState<ApiError | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const act = (run: () => Promise<unknown>) => {
    setActionError(null);
    run().catch((e: unknown) => {
      if (!(e instanceof WriteCancelled)) setActionError(toApiError(e));
    });
  };

  const latencyMode = latMode(o.latency);
  const lat = latencySpec(o.latency);
  const chance = lat?.chance;
  const fixed = lat?.fixed ?? "200ms";
  const range = lat && (lat.min !== undefined || lat.max !== undefined) ? { min: lat.min ?? "0s", max: lat.max ?? lat.min ?? "0s" } : null;
  const ms0 = (d: string) => parseDuration(d) ?? 0;

  const setMode = (m: LatMode) => {
    if (m === latencyMode) return;
    if (m === "none") adjust(latencyPatch(null));
    else if (m === "fixed") adjust(latencyPatch({ fixed: range ? range.min : "200ms", chance }));
    else {
      const base = lat?.fixed ?? "100ms";
      adjust(latencyPatch({ min: base, max: formatDuration(Math.max(ms0(base) * 2, 20)), chance }));
    }
  };

  const effects = declaredEffects(o);

  // What the rule uses that only advanced edits: in simple mode it becomes the
  // mark in the header, so that nothing stays hidden without notice.
  const features = advancedFeatures(o);
  const note = features.length ? advancedNote(features) : null;

  return (
    <li
      className={"ov" + (simple ? " ov--simple" : "") + (active ? " ov--active" : "") + (enabled ? "" : " ov--off")}
      aria-labelledby={`${ids}-name`}
    >
      <div className="ov__head">
        <Switch
          checked={enabled}
          label={`Rule ${o.name}`}
          onChange={(v) => change({ enabled: v })}
        />
        <span className="ov__name" id={`${ids}-name`}>
          {o.name}
        </span>
        {o.source ? (
          <span className={`prov prov--${o.source.kind}`} title={o.source.kind === "learned" ? "Created by learning mode from a real exchange" : "Derived from a captured exchange"}>
            {o.source.kind === "learned" ? "learned" : "derived"}
          </span>
        ) : null}
        {simple ? (
          <span className="ov__effect">
            <Effect o={o} />
          </span>
        ) : null}
        {simple && note ? (
          <button type="button" className="icon-button ov__adv" title={note} onClick={onAdvanced}>
            <SlidersIcon />
            <span className="sr-only">{`The rule ${o.name} ${note}`}</span>
          </button>
        ) : null}
        <LiveState enabled={enabled} active={active} never={never} state={state} now={now} at={at} busy={w.busy ? (guard.asking === doc.file ? "waiting for confirmation" : "saving") : null} stale={stale} compact={simple} onReset={reset} />
      </div>

      {simple ? null : (
        <p className="ov__sel">
          <span className="mono">
            <RuleSelector o={o} />
          </span>
          <span className="dim"> → </span>
          <Effect o={o} />
        </p>
      )}

      <div className="ov__controls">
        {effects.length === 0 ? (
          <p className="dim">
            No effect declared: this rule lets the requests it selects go to the destination.
          </p>
        ) : (
          effects.map((e) => (
            <Row
              key={e.kind}
              label={<EffectLabel o={o} kind={e.kind} />}
              htmlFor={`${ids}-${e.kind}`}
              hint={e.chance <= 0 ? <span className="tone-drop">never applies (0%)</span> : undefined}
            >
              <FrequencyControl
                id={`${ids}-${e.kind}`}
                label={`${EFFECT_NAME[e.kind]} frequency of ${o.name}`}
                value={e.chance}
                lead="on"
                tail="of calls"
                onChange={(p, delay) => adjust(chancePatch(o, e.kind, p), delay)}
                onSettle={w.flush}
              />
            </Row>
          ))
        )}
        {simple ? null : (
          <>
          <Row label="latency">
            <div className="lat">
              <Segmented<LatMode>
                label={`Latency of ${o.name}`}
                value={latencyMode}
                onChange={setMode}
                options={[
                  { value: "none", label: "none" },
                  { value: "fixed", label: "fixed" },
                  { value: "range", label: "range" },
                ]}
              />
              {latencyMode === "fixed" ? (
                <DurationSlider
                  label={`Fixed latency of ${o.name}`}
                  value={fixed}
                  onChange={(d, delay) => adjust(latencyPatch({ fixed: d, chance }), delay)}
                  onSettle={w.flush}
                />
              ) : null}
              {latencyMode === "range" && range ? (
                <>
                  <span className="lat__bound">
                    <span className="lat__label">min</span>
                    <DurationSlider
                      label={`Minimum latency of ${o.name}`}
                      onSettle={w.flush}
                      value={range.min}
                      onChange={(d, delay) =>
                        adjust(latencyPatch({ min: d, max: ms0(d) > ms0(range.max) ? d : range.max, chance }), delay)
                      }
                    />
                  </span>
                  <span className="lat__bound">
                    <span className="lat__label">max</span>
                    <DurationSlider
                      label={`Maximum latency of ${o.name}`}
                      onSettle={w.flush}
                      value={range.max}
                      onChange={(d, delay) =>
                        adjust(latencyPatch({ min: ms0(d) < ms0(range.min) ? d : range.min, max: d, chance }), delay)
                      }
                    />
                  </span>
                </>
              ) : null}
            </div>
          </Row>
          <Row label="drop">
            <span className="inline">
              <Switch
                checked={dropDeclared(o.drop)}
                label={`Drop the connection in ${o.name}`}
                onChange={(v) => adjust({ drop: v ? true : false })}
              />
              <span className={dropDeclared(o.drop) ? "tone-drop" : "dim"}>
                {dropDeclared(o.drop) ? "drops the connection without answering" : "responds normally"}
              </span>
            </span>
          </Row>
          </>
        )}
      </div>

      {o.source ? <SourceLine source={o.source} onShowExchange={onShowExchange} /> : null}

      {w.error ? <ErrorNote error={w.error} onDismiss={w.dismissError} what="The change was not saved" /> : null}
      {actionError ? <ErrorNote error={actionError} onDismiss={() => setActionError(null)} what="The action failed" /> : null}

      {simple ? null : (
        <details className="more">
          <summary>
            <ChevronIcon /> criteria, response and limits
          </summary>
          <OverrideForm
            o={o}
            ids={ids}
            change={change}
            rename={(name) =>
              act(() => guard.guard(doc, () => api.replaceOverride(route, override.name, { ...o, name })))
            }
          />
          <div className="more__actions">
            <button type="button" className="text-button" onClick={reset}>
              restart the TTL and the count
            </button>
            {confirmDelete ? (
              <span className="inline">
                <button
                  type="button"
                  className="text-button text-button--danger"
                  autoFocus
                  onClick={() => act(() => guard.guard(doc, () => api.deleteOverride(route, override.name)))}
                >
                  confirm removing {o.name}
                </button>
                <button type="button" className="text-button" onClick={() => setConfirmDelete(false)}>
                  keep
                </button>
              </span>
            ) : (
              <button type="button" className="text-button" onClick={() => setConfirmDelete(true)}>
                remove rule
              </button>
            )}
          </div>
        </details>
      )}
    </li>
  );
}

/** What one effect does, without its frequency: "responds 503", "delays 3s–30s", "drops". */
function EffectLabel({ o, kind }: { o: Override; kind: EffectKind }) {
  if (kind === "respond") {
    return (
      <>
        responds <span className="mono">{o.respond?.status ?? 200}</span>
      </>
    );
  }
  if (kind === "latency") {
    return (
      <>
        delays <span className="mono">{o.latency ? latencyText(o.latency) : ""}</span>
      </>
    );
  }
  return <>drops</>;
}

/** The effects of the rule in one line, each with its frequency when it is not every call. */
function Effect({ o }: { o: Override }) {
  const effects = declaredEffects(o);
  // "destination" only when there is nothing else to say: with a declared
  // effect, the summary is the effect, and going to the destination is what
  // is already expected.
  if (effects.length === 0) return <span className="dim">destination</span>;
  return (
    <>
      {effects.map((e, i) => (
        <span key={e.kind}>
          {i > 0 ? <span className="dim"> · </span> : null}
          <EffectMark o={o} e={e} />
        </span>
      ))}
    </>
  );
}

function EffectMark({ o, e }: { o: Override; e: DeclaredEffect }) {
  let body: ReactNode;
  if (e.kind === "respond") body = <span className="tone-drop mono">{o.respond?.status ?? 200}</span>;
  else if (e.kind === "latency") body = <span className="tone-injected mono">+{latencyText(o.latency!)}</span>;
  else body = <span className="tone-drop">drop</span>;
  return (
    <>
      {body}
      {e.chance === 1 ? null : (
        <span className="dim mono">
          {" · "}
          {percent(e.chance)}
        </span>
      )}
    </>
  );
}

function LiveState({
  enabled,
  active,
  never,
  state,
  now,
  at,
  busy,
  stale,
  compact,
  onReset,
}: {
  enabled: boolean;
  active: boolean;
  /** Every effect at 0%: the rule is on and within its limits, and still nothing happens. */
  never: boolean;
  state: OverrideLiveState | undefined;
  now: number;
  at: number;
  busy: string | null;
  /** No connection: the TTL and the applications are not being confirmed by the server. */
  stale: boolean;
  /**
   * Simple mode: only what runs out (deadline, limit, expiry). "Active" and
   * "off" are already said by the switch beside it, and repeating would be noise.
   */
  compact: boolean;
  onReset: () => void;
}) {
  const parts: ReactNode[] = [];
  if (!enabled) {
    if (!compact) parts.push(<span key="s">off</span>);
  } else if (never) {
    parts.push(
      <span key="s" className="ov__expired">
        never applies (0%)
      </span>,
    );
  } else if (state && !active) {
    const why =
      state.expired === "applications" || (state.maxApplications !== null && state.applications >= state.maxApplications)
        ? "ran out of applications"
        : "expired by TTL";
    parts.push(
      <span key="s" className="ov__expired">
        {why}{" "}
        <button type="button" className="link-button" onClick={onReset}>
          restart
        </button>
      </span>,
    );
  } else {
    if (!compact) {
      parts.push(
        <span key="s" className="ov__on">
          active
        </span>,
      );
    }
    if (state?.ttlRemainingMs != null) {
      parts.push(
        <span key="t" className="mono" title="Lifetime remaining">
          {seconds(state.ttlRemainingMs - (now - at))}
        </span>,
      );
    }
  }
  if (state && (state.maxApplications !== null || (!compact && state.applications > 0))) {
    parts.push(
      <span key="a" className="mono" title="Applications since the clock started">
        {state.applications}
        {state.maxApplications !== null ? `/${state.maxApplications}` : ""} appl.
      </span>,
    );
  }
  if (stale && enabled && state && (!compact || parts.length > 0)) {
    parts.push(
      <span key="p" title="No connection to /api/events: these numbers are from the last update and are not being confirmed">
        stopped
      </span>,
    );
  }
  return (
    <span className={"ov__live" + (stale ? " is-stale" : "")}>
      {busy ? <span className="ov__busy">{busy}</span> : null}
      {parts.map((p, i) => (
        <span key={i} className="ov__live-part">
          {p}
        </span>
      ))}
    </span>
  );
}

function SourceLine({
  source,
  onShowExchange,
}: {
  source: NonNullable<Override["source"]>;
  onShowExchange: (id: string) => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className="src">
      <p className="src__line">
        {source.kind === "learned" ? "learned" : "derived"} from exchange{" "}
        <span className="mono" title={source.exchange}>
          …{source.exchange.slice(-8)}
        </span>
        {source.at ? <span className="dim"> at {clock(source.at)}</span> : null}
        {source.bodyIncomplete ? <span className="tone-injected"> · body incomplete on capture</span> : null}{" "}
        <button type="button" className="link-button" aria-expanded={open} onClick={() => setOpen((v) => !v)}>
          {open ? "close the originating exchange" : "open the originating exchange"}
        </button>
      </p>
      {open ? <ExchangePeek id={source.exchange} onShowExchange={onShowExchange} /> : null}
    </div>
  );
}

/** The exchange this rule came from, read from the history if it is still there. */
function ExchangePeek({ id, onShowExchange }: { id: string; onShowExchange: (id: string) => void }) {
  const [ex] = useResource((s) => api.getExchange(id, s), [id]);
  if (ex.kind === "loading") return <p className="src__peek dim">reading the exchange…</p>;
  if (ex.kind === "error") {
    const e = ex.error;
    const text =
      e.code === "not_found"
        ? "The originating exchange has left the history (the backend keeps a limited number of exchanges)."
        : e.code === "history_disabled"
          ? "The history is disabled (history.expose), so the originating exchange cannot be read."
          : `The exchange could not be read: ${e.message}`;
    return <p className="src__peek dim">{text}</p>;
  }
  const x = ex.data;
  const ct = (h: Record<string, string[]> | undefined) => h?.["Content-Type"]?.[0] ?? h?.["content-type"]?.[0];
  return (
    <div className="src__peek">
      <p className="mono">
        {x.method} {x.path}
        {x.query ? "?" + x.query : ""} <span className="dim">→</span> {x.status || "no response"}{" "}
        <span className="dim">
          · {ms(x.timing.totalMs)} · {clock(x.start)}
        </span>
      </p>
      <p className="dim">
        request {x.request.size} B{ct(x.request.headers) ? ` (${ct(x.request.headers)})` : ""} · response{" "}
        {x.response.size} B{ct(x.response.headers) ? ` (${ct(x.response.headers)})` : ""}
        {x.response.truncated ? ", truncated on capture" : ""}
      </p>
      <p>
        <button type="button" className="link-button" onClick={() => onShowExchange(x.id)}>
          open the full exchange
        </button>
      </p>
    </div>
  );
}

/** Every field of the rule, written when each field loses focus. */
function OverrideForm({
  o,
  ids,
  change,
  rename,
}: {
  o: Override;
  ids: string;
  change: (p: MergePatch) => void;
  rename: (name: string) => void;
}) {
  const m = o.match;
  const r = o.respond;
  return (
    <div className="form">
      <Row label="name" htmlFor={`${ids}-name-in`}>
        <TextField
          id={`${ids}-name-in`}
          label="Rule name"
          mono
          size="sm"
          value={o.name}
          validate={(t) => (t.trim() ? null : "the name is required")}
          onCommit={(t) => rename(t.trim())}
        />
      </Row>

      <h4 className="form__group">criteria</h4>
      <Row label="path" htmlFor={`${ids}-path`} hint={PATH_HINT}>
        <TextField
          id={`${ids}-path`}
          label="Path"
          mono
          size="sm"
          value={m.path ?? ""}
          placeholder="any"
          validate={rulePathProblem}
          onCommit={(t) => change({ match: { path: t.trim() || null } })}
        />
      </Row>
      <Row label="path regex" htmlFor={`${ids}-rx`}>
        <TextField
          id={`${ids}-rx`}
          label="Path by regex"
          mono
          size="sm"
          value={m.pathRegex ?? ""}
          placeholder="none"
          validate={(t) => {
            if (!t.trim()) return null;
            try {
              new RegExp(t);
              return null;
            } catch {
              return "invalid regex";
            }
          }}
          onCommit={(t) => change({ match: { pathRegex: t.trim() || null } })}
        />
      </Row>
      <Row label="method" htmlFor={`${ids}-method`}>
        <TextField
          id={`${ids}-method`}
          label="Method"
          mono
          size="sm"
          list="http-methods"
          value={m.method ?? ""}
          placeholder="any"
          onCommit={(t) => change({ match: { method: t.trim().toUpperCase() || null } })}
        />
      </Row>
      <Row label="headers">
        <MapEditor
          label="Header criteria"
          keyPlaceholder="X-Tenant"
          entries={m.headers}
          onCommit={(next) => change({ match: { headers: next ? replaceWith(m.headers, next) : null } })}
        />
      </Row>
      <Row label="query">
        <MapEditor
          label="Query criteria"
          keyPlaceholder="retry"
          entries={m.query}
          onCommit={(next) => change({ match: { query: next ? replaceWith(m.query, next) : null } })}
        />
      </Row>
      <Row label="body">
        <BodyMatcherEditor
          label="Body criterion"
          value={m.body}
          onCommit={(next) => change({ match: { body: next === null ? null : replaceWith(m.body, next) } })}
        />
      </Row>

      <h4 className="form__group">response</h4>
      <Row label="synthesize">
        <span className="inline">
          <Switch
            checked={r !== undefined}
            label="Synthesize a response"
            onChange={(v) => change({ respond: v ? { status: 503 } : null })}
          />
          <span className="dim">{r ? "the gateway answers instead of the destination" : "forwards to the destination"}</span>
        </span>
      </Row>
      {r ? (
        <>
          <Row label="status" htmlFor={`${ids}-status`}>
            <IntField
              id={`${ids}-status`}
              label="Status"
              value={r.status}
              placeholder="200"
              min={100}
              max={599}
              onCommit={(n) => change({ respond: { status: n } })}
            />
          </Row>
          <Row label="headers">
            <MapEditor
              plain
              label="Response headers"
              keyPlaceholder="Retry-After"
              entries={r.headers}
              onCommit={(next) =>
                change({ respond: { headers: next ? replaceWith(r.headers, next) : null } })
              }
            />
          </Row>
          <Row label="body" htmlFor={`${ids}-body`} hint="valid JSON becomes a structure; anything else goes as text">
            <BodyText id={`${ids}-body`} value={r.body} onCommit={(b) => change({ respond: { body: b === null ? null : replaceWith(r.body, b) } })} />
          </Row>
        </>
      ) : null}

      <h4 className="form__group">limits</h4>
      <Row label="TTL" htmlFor={`${ids}-ttl`} hint="empty: no deadline">
        <DurationField id={`${ids}-ttl`} label="Lifetime" value={o.ttl} placeholder="no deadline" onCommit={(d) => change({ ttl: d })} />
      </Row>
      <Row label="max. applications" htmlFor={`${ids}-max`} hint="empty: no limit">
        <IntField
          id={`${ids}-max`}
          label="Application limit"
          value={o.maxApplications}
          placeholder="no limit"
          min={1}
          onCommit={(n) => change({ maxApplications: n })}
        />
      </Row>

      {o.source ? (
        <>
          <h4 className="form__group">origin</h4>
          <Row label="source">
            <span className="inline">
              <span className="mono dim">
                {o.source.kind} · {o.source.exchange}
              </span>
              <button type="button" className="text-button" onClick={() => change({ source: null })}>
                treat as declared
              </button>
            </span>
          </Row>
        </>
      ) : null}
    </div>
  );
}

/**
 * Patch that replaces a whole value: between two objects, the difference with
 * null on the keys that went away (otherwise the merge patch would add the old
 * one to the new); in any other case, the new value.
 */
function replaceWith(prev: unknown, next: unknown): unknown {
  const obj = (v: unknown) => typeof v === "object" && v !== null && !Array.isArray(v);
  return obj(prev) && obj(next) ? mergeDiff(prev, next) : next;
}

function BodyText({ id, value, onCommit }: { id: string; value: unknown; onCommit: (b: unknown) => void }) {
  const text = value === undefined ? "" : typeof value === "string" ? value : JSON.stringify(value, null, 2);
  const [draft, setDraft] = useState<string | null>(null);
  const shown = draft ?? text;
  return (
    <textarea
      id={id}
      className="input input--mono textarea"
      value={shown}
      rows={Math.min(8, Math.max(2, shown.split("\n").length))}
      spellCheck={false}
      placeholder="no body"
      onChange={(e) => setDraft(e.target.value)}
      onBlur={() => {
        if (draft === null || draft === text) {
          setDraft(null);
          return;
        }
        let next: unknown = draft;
        if (draft.trim() === "") next = null;
        else {
          try {
            next = JSON.parse(draft) as unknown;
          } catch {
            next = draft;
          }
        }
        setDraft(null);
        onCommit(next);
      }}
    />
  );
}
