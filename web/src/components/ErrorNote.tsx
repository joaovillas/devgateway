import type { ApiError } from "../api";
import { CloseIcon } from "./Icons";

/**
 * Write error, next to the control that caused it: the API message as it came,
 * the field and the position in the document when the API reports them.
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
          <button type="button" className="icon-button err__close" aria-label="Dismiss the error" onClick={onDismiss}>
            <CloseIcon />
          </button>
        ) : null}
      </p>
      <p>{b.message}</p>
      {b.field || b.env ? (
        <p className="dim">
          {b.field ? (
            <>
              field <span className="mono">{b.field}</span>
            </>
          ) : null}
          {b.file ? (
            <>
              {" "}
              in <span className="mono">{b.file}</span>
            </>
          ) : null}
          {b.line ? `, line ${b.line}${b.column ? `, column ${b.column}` : ""}` : null}
          {b.env ? (
            <>
              {b.field ? " · " : null}locked by the variable <span className="mono">{b.env}</span>
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
