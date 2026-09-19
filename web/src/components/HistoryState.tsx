import type { ReactNode } from "react";
import { api, type ApiError, type EffectiveValue } from "../api";
import { useResource } from "../hooks";
import { shortPath } from "../format";

/**
 * Where a configuration value comes from, read from GET /api/settings. It is
 * there to tell the developer exactly which key, file or variable controls what
 * they are seeing.
 */
function useSetting(key: string) {
  const [settings] = useResource((s) => api.settings(s), []);
  if (settings.kind !== "ready") return null;
  return settings.data.values.find((v) => v.key === key) ?? null;
}

function Origin({ value, fallbackEnv, fallbackFile }: { value: EffectiveValue | null; fallbackEnv?: string; fallbackFile?: string }) {
  const env = value?.source.origin === "env" ? (value.source.name ?? value.env) : fallbackEnv;
  if (env) {
    return (
      <>
        by the environment variable <span className="mono">{env}</span>, which beats <span className="mono">gateway.json</span>.
        To change it, remove or edit the variable and restart the process
      </>
    );
  }
  const file = value?.source.origin === "file" ? (value.source.name ?? "gateway.json") : fallbackFile;
  if (file) {
    return (
      <>
        in <span className="mono" title={file}>{shortPath(file)}</span>
      </>
    );
  }
  if (value?.source.origin === "default") {
    return (
      <>
        by the default value, because <span className="mono">gateway.json</span> does not declare the key
      </>
    );
  }
  return null;
}

function Recovery({ keyPath, value, locked }: { keyPath: "expose" | "record"; value: string; locked: boolean }) {
  if (locked) return null;
  return (
    <p>
      To turn it back on: <span className="mono">history.{keyPath}: {value}</span> in <span className="mono">gateway.json</span>,
      or <span className="mono">PATCH /api/settings</span> with{" "}
      <span className="mono">{`{"history":{"${keyPath}":${value}}}`}</span>. The panel follows the change on its own.
    </p>
  );
}

/** 403 history_disabled: the history exists, but reading it is off. */
export function HistoryDisabled({ error }: { error: ApiError }) {
  const setting = useSetting("history.expose");
  const locked = Boolean(error.body.env) || setting?.locked === true;
  return (
    <div className="state state--disabled" role="status">
      <p className="state__title">History disabled</p>
      <p>
        Reading the exchanges is off because of <span className="mono">history.expose = false</span>
        {" "}
        <Origin value={setting} fallbackEnv={error.body.env} fallbackFile={error.body.file} />. This is not an empty list:
        the gateway does not expose what it records.
      </p>
      <Recovery keyPath="expose" value="true" locked={locked} />
    </div>
  );
}

/** recording: false: reading works, but nothing new is recorded. */
export function RecordingOff({ compact, children }: { compact?: boolean; children?: ReactNode }) {
  const setting = useSetting("history.record");
  const origin = <Origin value={setting} />;
  if (compact) {
    return (
      <p className="notice notice--state" role="status">
        <strong>Recording off.</strong> <span className="mono">history.record = false</span>
        {setting ? <> {origin}</> : null}: the exchanges below are earlier ones, and new ones are not recorded.
      </p>
    );
  }
  return (
    <div className="state state--disabled" role="status">
      <p className="state__title">Recording off</p>
      <p>
        Requests are forwarded normally, but no exchange is recorded: <span className="mono">history.record = false</span>
        {setting ? <> {origin}</> : null}. That is why the list is empty, not for lack of traffic.
      </p>
      <Recovery keyPath="record" value="true" locked={setting?.locked === true} />
      {children}
    </div>
  );
}
