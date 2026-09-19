import type { ReactNode } from "react";
import type { ApiError } from "../api";

export function Loading({ what }: { what: string }) {
  return (
    <div className="state state--loading" role="status">
      <p className="state__title">Loading {what}</p>
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

/** API read failure, with the exact code and message it returned. */
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
        {network ? "No response from the admin port" : `Could not read ${what}`}
      </p>
      <p>
        <span className="mono">{request}</span>
        {network ? " received no response. " : " answered "}
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
          Field <span className="mono">{error.body.field}</span>
          {error.body.file ? (
            <>
              {" "}
              in <span className="mono">{error.body.file}</span>
              {error.body.line ? `, line ${error.body.line}` : null}
            </>
          ) : null}
          .
        </p>
      ) : null}
      {onRetry ? (
        <p>
          <button type="button" className="link-button" onClick={onRetry}>
            Try again
          </button>
        </p>
      ) : null}
    </div>
  );
}
