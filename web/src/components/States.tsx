import type { ReactNode } from "react";
import type { ApiError } from "../api";

export function Loading({ what }: { what: string }) {
  return (
    <div className="state state--loading" role="status">
      <p className="state__title">Carregando {what}</p>
    </div>
  );
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="state">
      <p className="state__title">{title}</p>
      {children}
    </div>
  );
}

/** Falha de leitura na API, com o código e a mensagem exatos que ela devolveu. */
export function Failure({
  what,
  request,
  error,
  onRetry,
}: {
  what: string;
  request: string;
  error: ApiError;
  onRetry?: () => void;
}) {
  const network = error.status === 0;
  return (
    <div className="state state--fault" role="alert">
      <p className="state__title">
        {network ? "Sem resposta da porta de administração" : `Não foi possível ler ${what}`}
      </p>
      <p>
        <span className="mono">{request}</span>
        {network ? " não recebeu resposta. " : " respondeu "}
        {network ? null : (
          <span className="mono">
            {error.status} {error.code}
          </span>
        )}
        {network ? null : ": "}
        {network ? null : error.message}
      </p>
      {error.body.field ? (
        <p>
          Campo <span className="mono">{error.body.field}</span>
          {error.body.file ? (
            <>
              {" "}
              em <span className="mono">{error.body.file}</span>
              {error.body.line ? `, linha ${error.body.line}` : null}
            </>
          ) : null}
          .
        </p>
      ) : null}
      {onRetry ? (
        <p>
          <button type="button" className="link-button" onClick={onRetry}>
            Tentar de novo
          </button>
        </p>
      ) : null}
    </div>
  );
}
