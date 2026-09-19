import { useState } from "react";
import type { ApiError, ConnectionState, LearningView, Status } from "../api";
import { toApiError } from "../hooks";
import { Switch } from "./Controls";
import { LockIcon } from "./Icons";
import type { Load } from "../hooks";
import { useNow } from "../hooks";
import { seconds } from "../format";

interface TopBarProps {
  /** New admin port after a write from this tab, or null. */
  movedTo: number | null;
  connection: ConnectionState;
  status: Load<Status>;
  onRetry: () => void;
  learning: Load<LearningView>;
  onLearning: (enabled: boolean) => Promise<unknown>;
}

/** Thin bar: connection to the process, history state and learning mode. */
export function TopBar({ movedTo, connection, status, onRetry, learning, onLearning }: TopBarProps) {
  const retrying = connection.kind === "retrying";
  const now = useNow(retrying && movedTo === null, 500);

  if (movedTo !== null) {
    const next = `${window.location.protocol}//${window.location.hostname}:${movedTo}/${window.location.hash}`;
    return (
      <header className="bar bar--offline">
        <span className="bar__name">gateway</span>
        <span className="bar__item bar__moved" role="alert">
          <span className="dot dot--fault" aria-hidden="true" />
          The admin port is now <span className="mono">:{movedTo}</span>; this page stopped updating.{" "}
          <a href={next}>Open the panel on :{movedTo}</a>
        </span>
      </header>
    );
  }

  return (
    <header className={"bar" + (retrying ? " bar--offline" : "")}>
      <span className="bar__name">gateway</span>

      <span className="bar__item" aria-live="polite">
        <span className="bar__label">connection</span>
        {connection.kind === "open" ? (
          <>
            <span className="dot dot--healthy" aria-hidden="true" />
            <span>live</span>
          </>
        ) : connection.kind === "connecting" ? (
          <>
            <span className="dot dot--idle dot--pulse" aria-hidden="true" />
            <span>{connection.attempt > 0 ? `reconnecting, attempt ${connection.attempt + 1}` : "connecting"}</span>
          </>
        ) : (
          <>
            <span className="dot dot--fault" aria-hidden="true" />
            <span className="bar__alert" title={connection.reason}>
              disconnected: the panel stopped updating. Next attempt in {seconds(connection.retryAt - now)}
            </span>
            <button type="button" className="link-button" onClick={onRetry}>
              try now
            </button>
          </>
        )}
      </span>

      <HistoryItem status={status} />

      <LearningItem learning={learning} onLearning={onLearning} />

      <span className="bar__spacer" />

      {status.kind === "ready" ? (
        <span className="bar__item bar__ports mono">
          <span className="bar__label">traffic</span>:{status.data.ports.traffic}
          <span className="bar__label">admin</span>:{status.data.ports.admin}
        </span>
      ) : null}
    </header>
  );
}

function HistoryItem({ status }: { status: Load<Status> }) {
  let body;
  if (status.kind === "loading") {
    body = <span className="dim">reading state</span>;
  } else if (status.kind === "error") {
    body = <span title={status.error.message}>state unavailable ({status.error.code})</span>;
  } else {
    const h = status.data.history;
    body = !h.expose ? (
      <span title="Reading the history is off in history.expose">
        disabled <span className="mono">(history.expose)</span>
      </span>
    ) : !h.record ? (
      <span title="New exchanges are not recorded: history.record is off">
        <span className="mono">{h.backend}</span> · recording off <span className="mono">(history.record)</span>
      </span>
    ) : (
      <span>
        <span className="mono">{h.backend}</span> · recording
      </span>
    );
  }
  return (
    <span className="bar__item">
      <span className="bar__label">history</span>
      {body}
    </span>
  );
}

/**
 * Learning mode in one gesture. When on, every request to a path without an
 * override becomes a learned, off override on the route (it shows up on its own
 * in the panel).
 */
function LearningItem({ learning, onLearning }: { learning: Load<LearningView>; onLearning: (enabled: boolean) => Promise<unknown> }) {
  const [pending, setPending] = useState<boolean | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  if (learning.kind !== "ready") {
    return (
      <span className="bar__item">
        <span className="bar__label">learning</span>
        <span className="dim">{learning.kind === "loading" ? "reading" : `unavailable (${learning.error.code})`}</span>
      </span>
    );
  }
  const l = learning.data;
  const on = pending ?? l.enabled;
  const envName = l.source.origin === "env" ? l.source.name : undefined;
  const total = Object.values(l.learned).reduce((a, b) => a + b, 0);
  return (
    <span className="bar__item">
      <span className="bar__label" id="learning-label">
        learning
      </span>
      <Switch
        checked={on}
        label="Learning mode"
        disabled={l.locked || pending !== null}
        title={
          l.locked
            ? `Locked by the environment variable ${envName ?? "GATEWAY_LEARNING"}`
            : "When on, every new path becomes a learned rule, off, in the service that received it"
        }
        onChange={(next) => {
          setPending(next);
          setError(null);
          onLearning(next).then(
            () => setPending(null),
            (e: unknown) => {
              setPending(null);
              setError(toApiError(e));
            },
          );
        }}
      />
      <span className={on ? "bar__on" : "dim"}>{on ? "on" : "off"}</span>
      {l.locked ? (
        <span className="bar__lock" title={`Locked by the environment variable ${envName ?? "GATEWAY_LEARNING"}`}>
          <LockIcon /> <span className="mono">{envName ?? "GATEWAY_LEARNING"}</span>
        </span>
      ) : null}
      {total > 0 ? <span className="dim">{total} {total === 1 ? "learned rule" : "learned rules"}</span> : null}
      {error ? (
        <span className="bar__alert" role="alert" title={error.message}>
          did not change: {error.message}
        </span>
      ) : null}
    </span>
  );
}
