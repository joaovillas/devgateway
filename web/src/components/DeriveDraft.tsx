import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { api, type Exchange, type Matcher, type Override, type RouteResource } from "../api";
import { WriteCancelled, type CommentsGuard } from "../commentsGuard";
import { PATH_HINT, rulePathProblem, shortPath } from "../format";
import { toApiError, type Load } from "../hooks";
import { FrequencyControl, Row, Switch } from "./Controls";
import { ErrorNote } from "./ErrorNote";
import { MapEditor } from "./MapEditor";
import type { ApiError } from "../api";

interface DeriveDraftProps {
  exchange: Exchange;
  routes: Load<RouteResource[]>;
  guard: CommentsGuard;
  onCancel: () => void;
  onCreated: (c: { route: string; name: string; enabled: boolean }) => void;
}

type Stage =
  | { kind: "loading" }
  | { kind: "error"; error: ApiError }
  | { kind: "ready"; warnings: string[] };

function bodyText(b: unknown): string {
  if (b === undefined || b === null) return "";
  return typeof b === "string" ? b : JSON.stringify(b, null, 2);
}

/** Valid JSON becomes a structure, as derive does; any other text goes as text. */
function parseBody(t: string): unknown {
  if (t.trim() === "") return undefined;
  try {
    return JSON.parse(t) as unknown;
  } catch {
    return t;
  }
}

function queryCriteria(q: string): Record<string, Matcher> {
  const out: Record<string, Matcher> = {};
  for (const [k, v] of new URLSearchParams(q)) if (!(k in out)) out[k] = v;
  return out;
}

/**
 * Draft of a rule assembled by the API from an exchange
 * (POST .../overrides/derive with save: false). Everything is editable here
 * and nothing is written until it is confirmed, which creates the override
 * with POST .../overrides.
 */
export function DeriveDraft({ exchange: x, routes, guard, onCancel, onCreated }: DeriveDraftProps) {
  const route = x.route!;
  const ids = useId();
  const [stage, setStage] = useState<Stage>({ kind: "loading" });
  const [draft, setDraft] = useState<Override | null>(null);
  const [body, setBody] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const nameRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    let live = true;
    api.deriveOverride(route, { exchange: x.id, save: false }).then(
      (r) => {
        if (!live) return;
        const d = "warnings" in r ? r : { override: r.override, warnings: [] as string[] };
        setDraft(d.override);
        setBody(bodyText(d.override.respond?.body));
        setStage({ kind: "ready", warnings: d.warnings ?? [] });
      },
      (e: unknown) => live && setStage({ kind: "error", error: toApiError(e) }),
    );
    return () => {
      live = false;
    };
  }, [route, x.id]);

  useEffect(() => {
    if (stage.kind === "ready") nameRef.current?.focus();
  }, [stage.kind]);

  const res = routes.kind === "ready" ? routes.data.find((r) => r.route.name === route) : undefined;

  if (stage.kind === "loading") {
    return (
      <div className="draft" role="status">
        <p className="draft__title">Assembling the draft from the exchange…</p>
      </div>
    );
  }
  if (stage.kind === "error" || !draft) {
    return (
      <div className="draft">
        {stage.kind === "error" ? <ErrorNote error={stage.error} what="The draft could not be assembled" /> : null}
        <p>
          <button type="button" className="button" onClick={onCancel}>
            Close
          </button>
        </p>
      </div>
    );
  }

  const set = (patch: Partial<Override>) => setDraft((d) => (d ? { ...d, ...patch } : d));
  const setMatch = (patch: Partial<Override["match"]>) => setDraft((d) => (d ? { ...d, match: { ...d.match, ...patch } } : d));
  const respond = draft.respond ?? {};
  const setRespond = (patch: Partial<NonNullable<Override["respond"]>>) =>
    setDraft((d) => (d ? { ...d, respond: { ...(d.respond ?? {}), ...patch } } : d));

  const incomplete = draft.source?.bodyIncomplete === true;
  const nameTaken = res?.route.overrides?.some((o) => o.name === draft.name.trim()) === true;
  const statusOk = respond.status === undefined || (respond.status >= 100 && respond.status <= 599);
  const pathProblem = rulePathProblem(draft.match.path ?? "");
  const observedQuery = x.query ? queryCriteria(x.query) : null;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!res) return;
    const name = draft.name.trim();
    const b = parseBody(body);
    const { body: _old, ...restRespond } = respond;
    const o: Override = {
      ...draft,
      name,
      enabled,
      respond: { ...restRespond, ...(b !== undefined ? { body: b } : {}) },
    };
    // An effect that holds on every call declares no frequency at all.
    if (o.respond && o.respond.chance === 1) delete o.respond.chance;
    setSending(true);
    setError(null);
    guard
      .guard({ file: res.file, hasComments: res.hasComments }, () => api.createOverride(route, o))
      .then(
        () => onCreated({ route, name, enabled }),
        (err: unknown) => {
          setSending(false);
          if (!(err instanceof WriteCancelled)) setError(toApiError(err));
        },
      );
  };

  return (
    <form className="draft" onSubmit={submit} aria-labelledby={`${ids}-t`}>
      <p className="draft__title" id={`${ids}-t`}>
        Draft rule in <span className="mono">{route}</span>
        <span className="draft__state">not saved</span>
      </p>
      <p className="hint">
        Filled in with this request and this response. Review and edit it; it only takes effect once it is created, and it
        goes to the end of the service's list, saved in <span className="mono" title={res?.file}>{res ? shortPath(res.file) : `routes/${route}.yaml`}</span>.
      </p>

      {incomplete || stage.warnings.length ? (
        <div className="body__cut" role="note">
          {incomplete ? (
            <p>
              <strong>Incomplete body:</strong> the response was truncated on capture, so the body below is not the whole
              of what the destination sent. Complete it before creating the rule, or create it as it is, knowing the
              response comes out truncated.
            </p>
          ) : null}
          {stage.warnings.length ? (
            <ul className="draft__warnings">
              {stage.warnings.map((w, i) => (
                <li key={i}>{w}</li>
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}

      <div className="form">
        <Row
          label="name"
          htmlFor={`${ids}-n`}
          hint={nameTaken ? <span className="field__problem">a rule with this name already exists in {route}</span> : undefined}
        >
          <input
            ref={nameRef}
            id={`${ids}-n`}
            className={"input input--mono input--sm" + (nameTaken ? " input--bad" : "")}
            required
            value={draft.name}
            spellCheck={false}
            aria-invalid={nameTaken || undefined}
            onChange={(e) => set({ name: e.target.value })}
          />
        </Row>

        <h4 className="form__group">criteria</h4>
        <Row
          label="path"
          htmlFor={`${ids}-p`}
          hint={
            pathProblem ? (
              <span className="field__problem" id={`${ids}-pe`}>
                {pathProblem}
              </span>
            ) : (
              `exact, as observed; ${PATH_HINT}`
            )
          }
        >
          <input
            id={`${ids}-p`}
            className={"input input--mono input--sm" + (pathProblem ? " input--bad" : "")}
            value={draft.match.path ?? ""}
            placeholder="any"
            spellCheck={false}
            aria-invalid={pathProblem ? true : undefined}
            aria-describedby={pathProblem ? `${ids}-pe` : undefined}
            onChange={(e) => setMatch({ path: e.target.value || undefined })}
          />
        </Row>
        <Row label="method" htmlFor={`${ids}-m`}>
          <input
            id={`${ids}-m`}
            className="input input--mono input--sm"
            list="http-methods"
            value={draft.match.method ?? ""}
            placeholder="any"
            spellCheck={false}
            onChange={(e) => setMatch({ method: e.target.value.toUpperCase() || undefined })}
          />
        </Row>
        <Row
          label="query"
          hint={
            observedQuery && !draft.match.query ? (
              <button type="button" className="link-button" onClick={() => setMatch({ query: observedQuery })}>
                use the observed query ({x.query})
              </button>
            ) : undefined
          }
        >
          <MapEditor
            label="Query criteria of the draft"
            keyPlaceholder="retry"
            entries={draft.match.query}
            onCommit={(next) => setMatch({ query: next })}
          />
        </Row>
        <Row label="headers">
          <MapEditor
            label="Header criteria of the draft"
            keyPlaceholder="X-Tenant"
            entries={draft.match.headers}
            onCommit={(next) => setMatch({ headers: next })}
          />
        </Row>

        <h4 className="form__group">response</h4>
        <Row label="status" htmlFor={`${ids}-s`} hint={statusOk ? undefined : <span className="field__problem">from 100 to 599</span>}>
          <input
            id={`${ids}-s`}
            className={"input input--mono input--sm input--num" + (statusOk ? "" : " input--bad")}
            inputMode="numeric"
            value={respond.status ?? ""}
            placeholder="200"
            onChange={(e) => {
              const t = e.target.value.replace(/\D/g, "");
              setRespond({ status: t ? Number(t) : undefined });
            }}
          />
        </Row>
        <Row label="headers">
          <MapEditor
            plain
            label="Response headers of the draft"
            keyPlaceholder="Content-Type"
            entries={respond.headers}
            onCommit={(next) => setRespond({ headers: next as Record<string, string> | undefined })}
          />
        </Row>
        <Row label="body" htmlFor={`${ids}-b`} hint="valid JSON becomes a structure; anything else goes as text">
          <textarea
            id={`${ids}-b`}
            className={"input input--mono textarea" + (incomplete ? " textarea--cut" : "")}
            value={body}
            rows={Math.min(10, Math.max(3, body.split("\n").length))}
            spellCheck={false}
            placeholder="no body"
            onChange={(e) => setBody(e.target.value)}
          />
        </Row>

        <h4 className="form__group">application</h4>
        <Row
          label={
            <>
              responds <span className="mono">{respond.status ?? 200}</span>
            </>
          }
          htmlFor={`${ids}-pr`}
          hint={(respond.chance ?? 1) <= 0 ? <span className="tone-drop">never applies (0%)</span> : undefined}
        >
          <FrequencyControl
            id={`${ids}-pr`}
            label="Response frequency of the draft"
            value={respond.chance ?? 1}
            lead="on"
            tail="of calls"
            onChange={(p) => setRespond({ chance: p })}
          />
        </Row>
        <Row label="on creation">
          <span className="inline">
            <Switch checked={enabled} label="Turn the rule on when creating it" onChange={setEnabled} />
            <span className="dim">
              {enabled ? "takes effect right away for the following requests" : "starts off"}
            </span>
          </span>
        </Row>
      </div>

      {error ? <ErrorNote error={error} onDismiss={() => setError(null)} what="The rule was not created" /> : null}
      <p className="inline draft__actions">
        <button
          type="submit"
          className="button button--primary"
          disabled={sending || !res || !draft.name.trim() || nameTaken || !statusOk || pathProblem !== null}
        >
          {sending ? "Creating…" : incomplete ? `Create in ${route} with the incomplete body` : `Create rule in ${route}`}
        </button>
        <button type="button" className="button" onClick={onCancel} disabled={sending}>
          Discard the draft
        </button>
      </p>
    </form>
  );
}
