import { Fragment, useEffect, useId, useRef, useState } from "react";
import {
  api,
  type ApiError,
  type EffectiveValue,
  type ReloadResult,
  type SettingsPatchResult,
  type SettingsView,
} from "../api";
import { bytes, shortPath } from "../format";
import { toApiError, useResource } from "../hooks";
import type { DetailMode } from "../mode";
import { patchForKey } from "../patch";
import { IntField, Segmented, Switch, TextField } from "./Controls";
import { ErrorNote } from "./ErrorNote";
import { LockIcon } from "./Icons";
import { Failure, Loading } from "./States";

// The process settings (gateway.json), key by key, in the order of
// GET /api/settings. Each control saves through PATCH /api/settings, which
// applies hot; a value coming from the environment shows up locked with the
// name of the variable.

const LABELS: Record<string, { label: string; hint?: string }> = {
  "ports.traffic": { label: "traffic port", hint: "reopens the listener; connections in flight end on the old port" },
  "ports.admin": { label: "admin port", hint: "the panel moves to the new port" },
  seed: { label: "seed", hint: "empty: the draw is not reproducible" },
  "history.backend": {
    label: "history backend",
    hint: "switching does not migrate the exchanges already recorded; the arrows only choose, Space or Enter applies",
  },
  "history.path": { label: "history file", hint: "for ndjson and sqlite" },
  "history.capacity": { label: "history capacity", hint: "exchanges kept" },
  "history.record": { label: "history recording" },
  "history.expose": { label: "history exposure" },
  "capture.maxBodyBytes": { label: "captured body, maximum", hint: "in bytes; the excess is truncated" },
  "learning.enabled": { label: "learning mode" },
  routesDir: { label: "services directory" },
};

/** The effective value in a single word, for reading in the simple mode. */
function settingText(v: EffectiveValue): string {
  if (typeof v.value === "boolean") return v.value ? "on" : "off";
  if (v.value === null || v.value === "") return "—";
  if (v.key === "capture.maxBodyBytes" && typeof v.value === "number") return bytes(v.value);
  return String(v.value);
}

export function ProcessBody({
  version,
  onSettings,
  mode,
  onAdvanced,
}: {
  version: number;
  /** Result of a settings write (the admin port may have changed). */
  onSettings?: (r: SettingsPatchResult) => void;
  /** Simple reads the effective values; advanced edits each one. */
  mode: DetailMode;
  onAdvanced: () => void;
}) {
  // Settings event: re-read while keeping the controls in view. Going back to
  // "loading" would unmount the fields and lose what is being typed.
  const [settings, reload] = useResource((s) => api.settings(s), []);
  const firstVersion = useRef(version);
  useEffect(() => {
    if (version !== firstVersion.current) reload();
  }, [version, reload]);
  // The PATCH response already carries the new settings; it holds until the re-read.
  const [latest, setLatest] = useState<SettingsView | null>(null);
  useEffect(() => setLatest(null), [settings]);
  const [notes, setNotes] = useState<string[]>([]);
  const [error, setError] = useState<{ key: string; error: ApiError } | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  if (settings.kind === "loading") return <Loading what="the process settings" />;
  if (settings.kind === "error") {
    return <Failure what="the settings" request="GET /api/settings" error={settings.error} onRetry={reload} />;
  }
  const view = latest ?? settings.data;

  // Simple mode: the same values, only to read. Nothing leaves the screen, but
  // changing a port, the history backend or the seed is a gesture of the advanced mode.
  if (mode === "simple") {
    return (
      <div className="proc">
        <p className="proc__file">
          Effective values of <span className="mono">{view.file.path}</span>
          {view.file.exists ? "" : " (the file does not exist yet: everything is at the default or in the environment)"}.
        </p>
        <dl className="uph">
          {view.values.map((v) => (
            <Fragment key={v.key}>
              <dt>{LABELS[v.key]?.label ?? v.key}</dt>
              <dd className="mono">
                {settingText(v)}
                {v.locked ? (
                  <span className="origin origin--env proc__lock" title={`Set by ${v.env}`}>
                    <LockIcon /> {v.env}
                  </span>
                ) : null}
              </dd>
            </Fragment>
          ))}
        </dl>
        <p>
          <button type="button" className="text-button" onClick={onAdvanced}>
            open advanced to edit
          </button>
        </p>
      </div>
    );
  }

  const commit = (key: string, value: unknown) => {
    setBusy(key);
    setError(null);
    api.patchSettings(patchForKey(key, value)).then(
      (r) => {
        setBusy(null);
        setLatest(r.settings);
        setNotes(r.notes);
        onSettings?.(r);
      },
      (e: unknown) => {
        setBusy(null);
        setError({ key, error: toApiError(e) });
      },
    );
  };

  return (
    <div className="proc">
      <p className="proc__file">
        {view.file.exists ? (
          <>
            Effective values. What comes from <span className="mono">{view.file.path}</span> changes here and takes
            effect right away.
          </>
        ) : (
          <>
            <span className="mono">{view.file.path}</span> does not exist yet: everything is at the default or in the
            environment. The first change creates the file.
          </>
        )}
      </p>
      <div className="settings" role="list">
        {view.values.map((v) => (
          <Setting
            key={v.key}
            v={v}
            busy={busy === v.key}
            error={error?.key === v.key ? error.error : null}
            onDismiss={() => setError(null)}
            onCommit={(value) => commit(v.key, value)}
          />
        ))}
      </div>
      {notes.length ? (
        <ul className="proc__notes" aria-live="polite">
          {notes.map((n, i) => (
            <li key={i}>{n}</li>
          ))}
        </ul>
      ) : null}
      <ReloadFromDisk />
    </div>
  );
}

function Origin({ v }: { v: EffectiveValue }) {
  if (v.locked || v.source.origin === "env") {
    return (
      <span className="origin origin--env" title={`Set by the environment variable ${v.source.name ?? v.env}, which beats gateway.json. To change it, alter the variable and restart the process.`}>
        <LockIcon /> <span className="mono">{v.source.name ?? v.env}</span>
      </span>
    );
  }
  if (v.source.origin === "file") {
    const file = v.source.name ?? "gateway.json";
    return (
      <span className="origin mono" title={file}>
        {shortPath(file)}
      </span>
    );
  }
  return <span className="origin origin--default">default</span>;
}

function Setting({
  v,
  busy,
  error,
  onDismiss,
  onCommit,
}: {
  v: EffectiveValue;
  busy: boolean;
  error: ApiError | null;
  onDismiss: () => void;
  onCommit: (value: unknown) => void;
}) {
  const id = useId();
  const meta = LABELS[v.key] ?? { label: v.key };
  const locked = v.locked;
  const hintId = `${id}-h`;
  return (
    <div className={"setting" + (locked ? " setting--locked" : "")} role="listitem">
      <label className="setting__label" htmlFor={id}>
        {meta.label}
        <span className="setting__key mono">{v.key}</span>
      </label>
      <div className="setting__ctl">
        <Control v={v} id={id} locked={locked} onCommit={onCommit} describedBy={hintId} />
        {busy ? <span className="dim">applying</span> : null}
      </div>
      <div className="setting__origin">
        <Origin v={v} />
        {v.source.origin === "file" && !locked ? (
          <button
            type="button"
            className="text-button"
            title={`Removes ${v.key} from gateway.json and goes back to the default value`}
            onClick={() => onCommit(null)}
          >
            use the default
          </button>
        ) : null}
      </div>
      <p className="setting__hint" id={hintId}>
        {locked ? (
          <>
            Locked by <span className="mono">{v.source.name ?? v.env}</span>. The API refuses to change it; alter the
            variable and restart.
          </>
        ) : (
          meta.hint
        )}
      </p>
      {error ? <ErrorNote error={error} onDismiss={onDismiss} what={`${v.key} was not changed`} /> : null}
    </div>
  );
}

function Control({
  v,
  id,
  locked,
  onCommit,
  describedBy,
}: {
  v: EffectiveValue;
  id: string;
  locked: boolean;
  onCommit: (value: unknown) => void;
  describedBy: string;
}) {
  const label = LABELS[v.key]?.label ?? v.key;
  if (typeof v.value === "boolean") {
    return (
      <span className="inline">
        <Switch id={id} checked={v.value} label={label} disabled={locked} describedBy={describedBy} onChange={(b) => onCommit(b)} />
        <span className="dim">{v.value ? "on" : "off"}</span>
      </span>
    );
  }
  if (v.key === "history.backend") {
    const value = String(v.value) as "memory" | "ndjson" | "sqlite";
    return (
      <span id={id}>
        <Segmented
          label={label}
          value={value}
          disabled={locked}
          manual
          onChange={(b) => onCommit(b)}
          options={[
            { value: "memory", label: "memory" },
            { value: "ndjson", label: "ndjson" },
            { value: "sqlite", label: "sqlite" },
          ]}
        />
      </span>
    );
  }
  if (typeof v.value === "number" || v.value === null || v.key === "seed") {
    const isPort = v.key.startsWith("ports.");
    return (
      <span className="inline">
        <IntField
          id={id}
          label={label}
          value={v.value as number | null}
          disabled={locked}
          min={isPort ? 1 : 0}
          max={isPort ? 65535 : undefined}
          placeholder={v.key === "seed" ? "random" : undefined}
          describedBy={describedBy}
          onCommit={(n) => onCommit(n)}
        />
        {v.key === "capture.maxBodyBytes" && typeof v.value === "number" ? (
          <span className="dim mono setting__unit">{bytes(v.value)}</span>
        ) : null}
      </span>
    );
  }
  return (
    <TextField
      id={id}
      label={label}
      mono
      size="sm"
      value={String(v.value ?? "")}
      disabled={locked}
      describedBy={describedBy}
      onCommit={(t) => onCommit(t.trim() === "" ? null : t.trim())}
    />
  );
}

/** Parity with editing the files outside the panel: POST /api/reload. */
function ReloadFromDisk() {
  const [result, setResult] = useState<ReloadResult | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [busy, setBusy] = useState(false);
  return (
    <div className="proc__reload">
      <button
        type="button"
        className="button"
        disabled={busy}
        onClick={() => {
          setBusy(true);
          setError(null);
          api.reload().then(
            (r) => {
              setBusy(false);
              setResult(r);
            },
            (e: unknown) => {
              setBusy(false);
              setError(toApiError(e));
            },
          );
        }}
      >
        Re-read the files from disk
      </button>
      <span className="dim">for when gateway.json or routes/ were edited outside the panel</span>
      {result ? (
        <p className="proc__notes" aria-live="polite">
          {result.routes} {result.routes === 1 ? "service loaded" : "services loaded"}.
          {result.changed.routes.length ? ` Changed services: ${result.changed.routes.join(", ")}.` : " No service changed."}
          {result.changed.settings.length ? ` Settings: ${result.changed.settings.join(", ")}.` : ""}
          {result.warnings.map((w, i) => (
            <span key={i} className="dim">
              {" "}
              {w}.
            </span>
          ))}
        </p>
      ) : null}
      {error ? <ErrorNote error={error} onDismiss={() => setError(null)} what="The re-read failed; the previous settings remain" /> : null}
    </div>
  );
}
