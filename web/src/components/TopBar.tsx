import type { ConnectionState, Status } from "../api";
import type { Load } from "../hooks";
import { useNow } from "../hooks";
import { seconds } from "../format";

interface TopBarProps {
  connection: ConnectionState;
  status: Load<Status>;
  onRetry: () => void;
}

/** Barra fina: conexão com o processo e estado do histórico, nada além. */
export function TopBar({ connection, status, onRetry }: TopBarProps) {
  const retrying = connection.kind === "retrying";
  const now = useNow(retrying, 500);

  return (
    <header className="bar">
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
            <span>conectando</span>
          </>
        ) : (
          <>
            <span className="dot dot--fault" aria-hidden="true" />
            <span title={connection.reason}>
              desconectada, nova tentativa em {seconds(connection.retryAt - now)}
            </span>
            <button type="button" className="link-button" onClick={onRetry}>
              tentar agora
            </button>
          </>
        )}
      </span>

      <HistoryItem status={status} />

      <span className="bar__spacer" />

      {status.kind === "ready" ? (
        <span className="bar__item mono">
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
      <span>
        desabilitado <span className="mono">(history.expose)</span>
      </span>
    ) : (
      <span>
        <span className="mono">{h.backend}</span> · {h.record ? "gravando" : "registro desligado"}
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
