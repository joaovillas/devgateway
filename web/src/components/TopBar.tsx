import { useState } from "react";
import type { ApiError, ConnectionState, LearningView, Status } from "../api";
import { toApiError } from "../hooks";
import { Switch } from "./Controls";
import { LockIcon } from "./Icons";
import type { Load } from "../hooks";
import { useNow } from "../hooks";
import { seconds } from "../format";

interface TopBarProps {
  /** Porta de administração nova depois de uma escrita desta aba, ou null. */
  movedTo: number | null;
  connection: ConnectionState;
  status: Load<Status>;
  onRetry: () => void;
  learning: Load<LearningView>;
  onLearning: (enabled: boolean) => Promise<unknown>;
}

/** Barra fina: conexão com o processo, estado do histórico e o modo aprendizado. */
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
          A porta de administração passou a ser <span className="mono">:{movedTo}</span>; esta página parou de
          atualizar. <a href={next}>Abrir o painel em :{movedTo}</a>
        </span>
      </header>
    );
  }

  return (
    <header className={"bar" + (retrying ? " bar--offline" : "")}>
      <span className="bar__name">gateway</span>

      <span className="bar__item" aria-live="polite">
        <span className="bar__label">conexão</span>
        {connection.kind === "open" ? (
          <>
            <span className="dot dot--healthy" aria-hidden="true" />
            <span>ao vivo</span>
          </>
        ) : connection.kind === "connecting" ? (
          <>
            <span className="dot dot--idle dot--pulse" aria-hidden="true" />
            <span>{connection.attempt > 0 ? `reconectando, tentativa ${connection.attempt + 1}` : "conectando"}</span>
          </>
        ) : (
          <>
            <span className="dot dot--fault" aria-hidden="true" />
            <span className="bar__alert" title={connection.reason}>
              desconectada: o painel parou de atualizar. Nova tentativa em {seconds(connection.retryAt - now)}
            </span>
            <button type="button" className="link-button" onClick={onRetry}>
              tentar agora
            </button>
          </>
        )}
      </span>

      <HistoryItem status={status} />

      <LearningItem learning={learning} onLearning={onLearning} />

      <span className="bar__spacer" />

      {status.kind === "ready" ? (
        <span className="bar__item bar__ports mono">
          <span className="bar__label">tráfego</span>:{status.data.ports.traffic}
          <span className="bar__label">admin</span>:{status.data.ports.admin}
        </span>
      ) : null}
    </header>
  );
}

function HistoryItem({ status }: { status: Load<Status> }) {
  let body;
  if (status.kind === "loading") {
    body = <span className="dim">lendo estado</span>;
  } else if (status.kind === "error") {
    body = <span title={status.error.message}>estado indisponível ({status.error.code})</span>;
  } else {
    const h = status.data.history;
    body = !h.expose ? (
      <span title="A leitura do histórico está desligada em history.expose">
        desabilitado <span className="mono">(history.expose)</span>
      </span>
    ) : !h.record ? (
      <span title="As trocas novas não são gravadas: history.record está desligado">
        <span className="mono">{h.backend}</span> · registro desligado <span className="mono">(history.record)</span>
      </span>
    ) : (
      <span>
        <span className="mono">{h.backend}</span> · gravando
      </span>
    );
  }
  return (
    <span className="bar__item">
      <span className="bar__label">histórico</span>
      {body}
    </span>
  );
}

/**
 * Modo aprendizado em um gesto. Ligado, cada requisição a um path sem override
 * vira um override aprendido e desligado na rota (aparece sozinho no painel).
 */
function LearningItem({ learning, onLearning }: { learning: Load<LearningView>; onLearning: (enabled: boolean) => Promise<unknown> }) {
  const [pending, setPending] = useState<boolean | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  if (learning.kind !== "ready") {
    return (
      <span className="bar__item">
        <span className="bar__label">aprendizado</span>
        <span className="dim">{learning.kind === "loading" ? "lendo" : `indisponível (${learning.error.code})`}</span>
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
        aprendizado
      </span>
      <Switch
        checked={on}
        label="Modo aprendizado"
        disabled={l.locked || pending !== null}
        title={
          l.locked
            ? `Travado pela variável de ambiente ${envName ?? "GATEWAY_LEARNING"}`
            : "Ligado, cada path novo vira um override aprendido e desligado na rota"
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
      <span className={on ? "bar__on" : "dim"}>{on ? "ligado" : "desligado"}</span>
      {l.locked ? (
        <span className="bar__lock" title={`Travado pela variável de ambiente ${envName ?? "GATEWAY_LEARNING"}`}>
          <LockIcon /> <span className="mono">{envName ?? "GATEWAY_LEARNING"}</span>
        </span>
      ) : null}
      {total > 0 ? <span className="dim">{total} {total === 1 ? "aprendido" : "aprendidos"}</span> : null}
      {error ? (
        <span className="bar__alert" role="alert" title={error.message}>
          não mudou: {error.message}
        </span>
      ) : null}
    </span>
  );
}
