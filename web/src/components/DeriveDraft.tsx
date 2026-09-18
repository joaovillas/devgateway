import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { api, type Exchange, type Matcher, type Override, type RouteResource } from "../api";
import { WriteCancelled, type CommentsGuard } from "../commentsGuard";
import { toApiError, type Load } from "../hooks";
import { ProbabilityControl, Row, Switch } from "./Controls";
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

/** JSON válido vira estrutura, como o derive faz; qualquer outro texto vai como texto. */
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
 * Rascunho de override montado pela API a partir de uma troca
 * (POST .../overrides/derive com save: false). Tudo é editável aqui e nada é
 * gravado até a confirmação, que cria o override com POST .../overrides.
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
        <p className="draft__title">Montando o rascunho a partir da troca…</p>
      </div>
    );
  }
  if (stage.kind === "error" || !draft) {
    return (
      <div className="draft">
        {stage.kind === "error" ? <ErrorNote error={stage.error} what="O rascunho não pôde ser montado" /> : null}
        <p>
          <button type="button" className="button" onClick={onCancel}>
            Fechar
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
    if (o.probability === 1) delete o.probability;
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
        Rascunho de override em <span className="mono">{route}</span>
        <span className="draft__state">não gravado</span>
      </p>
      <p className="hint">
        Preenchido com esta requisição e esta resposta. Revise e edite; ele só passa a valer quando for criado, e entra no
        fim da lista da rota, gravado em <span className="mono">{res?.file ?? `routes/${route}.yaml`}</span>.
      </p>

      {incomplete || stage.warnings.length ? (
        <div className="body__cut" role="note">
          {incomplete ? (
            <p>
              <strong>Corpo incompleto:</strong> a resposta foi cortada na captura, então o corpo abaixo não é o que o
              upstream mandou por inteiro. Complete-o antes de criar, ou crie assim sabendo que a resposta sai cortada.
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
          label="nome"
          htmlFor={`${ids}-n`}
          hint={nameTaken ? <span className="field__problem">já existe um override com esse nome em {route}</span> : undefined}
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

        <h4 className="form__group">critérios</h4>
        <Row label="path" htmlFor={`${ids}-p`} hint="exato, como observado; curinga de sufixo: /api/x/*">
          <input
            id={`${ids}-p`}
            className="input input--mono input--sm"
            value={draft.match.path ?? ""}
            placeholder="qualquer"
            spellCheck={false}
            onChange={(e) => setMatch({ path: e.target.value || undefined })}
          />
        </Row>
        <Row label="método" htmlFor={`${ids}-m`}>
          <input
            id={`${ids}-m`}
            className="input input--mono input--sm"
            list="http-methods"
            value={draft.match.method ?? ""}
            placeholder="qualquer"
            spellCheck={false}
            onChange={(e) => setMatch({ method: e.target.value.toUpperCase() || undefined })}
          />
        </Row>
        <Row
          label="query"
          hint={
            observedQuery && !draft.match.query ? (
              <button type="button" className="link-button" onClick={() => setMatch({ query: observedQuery })}>
                usar a query observada ({x.query})
              </button>
            ) : undefined
          }
        >
          <MapEditor
            label="Critérios de query do rascunho"
            keyPlaceholder="retry"
            entries={draft.match.query}
            onCommit={(next) => setMatch({ query: next })}
          />
        </Row>
        <Row label="cabeçalhos">
          <MapEditor
            label="Critérios de cabeçalho do rascunho"
            keyPlaceholder="X-Tenant"
            entries={draft.match.headers}
            onCommit={(next) => setMatch({ headers: next })}
          />
        </Row>

        <h4 className="form__group">resposta</h4>
        <Row label="status" htmlFor={`${ids}-s`} hint={statusOk ? undefined : <span className="field__problem">de 100 a 599</span>}>
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
        <Row label="cabeçalhos">
          <MapEditor
            plain
            label="Cabeçalhos da resposta do rascunho"
            keyPlaceholder="Content-Type"
            entries={respond.headers}
            onCommit={(next) => setRespond({ headers: next as Record<string, string> | undefined })}
          />
        </Row>
        <Row label="corpo" htmlFor={`${ids}-b`} hint="JSON válido vira estrutura; o resto vai como texto">
          <textarea
            id={`${ids}-b`}
            className={"input input--mono textarea" + (incomplete ? " textarea--cut" : "")}
            value={body}
            rows={Math.min(10, Math.max(3, body.split("\n").length))}
            spellCheck={false}
            placeholder="sem corpo"
            onChange={(e) => setBody(e.target.value)}
          />
        </Row>

        <h4 className="form__group">aplicação</h4>
        <Row label="probabilidade" htmlFor={`${ids}-pr`}>
          <ProbabilityControl
            id={`${ids}-pr`}
            label="Probabilidade do rascunho"
            value={draft.probability ?? 1}
            onChange={(p) => set({ probability: p })}
          />
        </Row>
        <Row label="ao criar">
          <span className="inline">
            <Switch checked={enabled} label="Ligar o override ao criar" onChange={setEnabled} />
            <span className="dim">
              {enabled ? "passa a valer na hora para as requisições seguintes" : "nasce desligado"}
            </span>
          </span>
        </Row>
      </div>

      {error ? <ErrorNote error={error} onDismiss={() => setError(null)} what="O override não foi criado" /> : null}
      <p className="inline draft__actions">
        <button
          type="submit"
          className="button button--primary"
          disabled={sending || !res || !draft.name.trim() || nameTaken || !statusOk}
        >
          {sending ? "Criando…" : incomplete ? `Criar em ${route} com o corpo incompleto` : `Criar override em ${route}`}
        </button>
        <button type="button" className="button" onClick={onCancel} disabled={sending}>
          Descartar o rascunho
        </button>
      </p>
    </form>
  );
}
