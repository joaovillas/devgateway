import type { ApiError } from "../api";
import { CloseIcon } from "./Icons";

/**
 * Erro de escrita, junto do controle que o causou: a mensagem da API como
 * veio, o campo e a posição no documento quando a API os informa.
 */
export function ErrorNote({ error, onDismiss, what }: { error: ApiError; onDismiss?: () => void; what: string }) {
  const b = error.body;
  const details = b.errors && b.errors.length > 1 ? b.errors : null;
  return (
    <div className="err" role="alert">
      <p className="err__title">
        {what}
        {error.status ? (
          <span className="mono dim">
            {" "}
            {error.status} {error.code}
          </span>
        ) : null}
        {onDismiss ? (
          <button type="button" className="icon-button err__close" aria-label="Dispensar o erro" onClick={onDismiss}>
            <CloseIcon />
          </button>
        ) : null}
      </p>
      <p>{b.message}</p>
      {b.field || b.env ? (
        <p className="dim">
          {b.field ? (
            <>
              campo <span className="mono">{b.field}</span>
            </>
          ) : null}
          {b.file ? (
            <>
              {" "}
              em <span className="mono">{b.file}</span>
            </>
          ) : null}
          {b.line ? `, linha ${b.line}${b.column ? `, coluna ${b.column}` : ""}` : null}
          {b.env ? (
            <>
              {b.field ? " · " : null}travado pela variável <span className="mono">{b.env}</span>
            </>
          ) : null}
        </p>
      ) : null}
      {details ? (
        <ul className="err__list">
          {details.map((d, i) => (
            <li key={i}>
              {d.field ? <span className="mono">{d.field}: </span> : null}
              {d.message}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
