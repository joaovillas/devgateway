import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { api, isApiError, type ApiError, type RouteResource, type SettingsPatchResult, type VersionedText } from "../api";
import { toApiError, useResource } from "../hooks";
import { ErrorNote } from "./ErrorNote";
import { Panel } from "./Panel";
import { Empty, Failure, Loading } from "./States";

/** O documento ao lado dos controles: o YAML da rota ou o gateway.json do processo. */
export type DocTarget =
  | { kind: "route"; name: string; route: RouteResource | undefined }
  | { kind: "process" }
  | null;

interface DocumentPanelProps {
  target: DocTarget;
  /** Muda a cada evento de configuração, para reler o documento. */
  version: number;
  /** Resultado de gravar o gateway.json (a porta de administração pode ter mudado). */
  onSettings?: (r: SettingsPatchResult) => void;
}

export function DocumentPanel({ target, version, onSettings }: DocumentPanelProps) {
  const file = target?.kind === "process" ? "gateway.json" : target?.route?.file;
  return (
    <Panel id="doc" title="documento" sub={file ? <span className="mono">{file}</span> : undefined}>
      {target ? (
        <DocumentEditor
          key={target.kind === "route" ? `r:${target.name}` : "p"}
          target={target}
          version={version}
          onSettings={onSettings}
        />
      ) : (
        <Empty title="Nenhum documento aberto">
          <p>
            O documento da rota selecionada aparece aqui, como está no disco, e pode ser editado direto. A aba{" "}
            <span className="mono">processo</span> abre o <span className="mono">gateway.json</span>.
          </p>
        </Empty>
      )}
    </Panel>
  );
}

function DocumentEditor({
  target,
  version,
  onSettings,
}: {
  target: NonNullable<DocTarget>;
  version: number;
  onSettings?: (r: SettingsPatchResult) => void;
}) {
  // Notas do servidor sobre a última gravação do gateway.json (porta reaberta,
  // backend trocado sem migrar o histórico...).
  const [notes, setNotes] = useState<string[]>([]);
  const isRoute = target.kind === "route";
  const name = isRoute ? target.name : "";
  const [doc, reload] = useResource<VersionedText>(
    (signal) => (isRoute ? api.getRouteDocument(name, signal) : api.getSettingsDocument(signal)),
    [target.kind, name],
  );
  // Evento de configuração: relê sem voltar a "carregando", para o texto não
  // piscar e a margem poder marcar as linhas que mudaram.
  const firstVersion = useRef(version);
  useEffect(() => {
    if (version !== firstVersion.current) reload();
  }, [version, reload]);

  if (doc.kind === "loading") return <Loading what="o documento" />;
  if (doc.kind === "error") {
    return (
      <Failure
        what="o documento"
        request={isRoute ? `GET /api/routes/${name}/document` : "GET /api/settings/document"}
        error={doc.error}
        onRetry={reload}
      />
    );
  }
  return (
    <Editor
      notes={notes}
      doc={doc.data}
      lang={isRoute ? "yaml" : "json"}
      label={isRoute ? (target.route?.file ?? `routes/${name}.yaml`) : "gateway.json"}
      hasComments={isRoute ? target.route?.hasComments === true : false}
      save={(text, ifMatch) =>
        isRoute
          ? api.putRouteDocument(name, text, ifMatch).then(reload)
          : api.putSettingsDocument(text, ifMatch).then((r) => {
              setNotes(r.notes ?? []);
              onSettings?.(r);
              reload();
            })
      }
      request={isRoute ? `PUT /api/routes/${name}/document` : "PUT /api/settings/document"}
    />
  );
}

/** Linhas de `next` que não estão na maior subsequência comum com `prev`. */
function changedLines(prev: string[], next: string[]): Set<number> {
  const n = prev.length;
  const m = next.length;
  if (n * m > 400_000) return new Set();
  const dp: Uint16Array[] = Array.from({ length: n + 1 }, () => new Uint16Array(m + 1));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i]![j] = prev[i] === next[j] ? dp[i + 1]![j + 1]! + 1 : Math.max(dp[i + 1]![j]!, dp[i]![j + 1]!);
    }
  }
  const out = new Set<number>();
  let i = 0;
  let j = 0;
  while (j < m) {
    if (i < n && prev[i] === next[j]) {
      i++;
      j++;
    } else if (i < n && dp[i + 1]![j]! >= dp[i]![j + 1]!) {
      i++;
    } else {
      out.add(j);
      j++;
    }
  }
  return out;
}

function Editor({
  notes,
  doc,
  lang,
  label,
  hasComments,
  save,
  request,
}: {
  notes: string[];
  doc: VersionedText;
  lang: "yaml" | "json";
  label: string;
  hasComments: boolean;
  save: (text: string, ifMatch?: string) => Promise<unknown>;
  request: string;
}) {
  // Rascunho: null enquanto o texto é o do disco. `baseEtag` é a versão sobre
  // a qual o rascunho começou, mandada em If-Match.
  const [draft, setDraft] = useState<string | null>(null);
  const [baseEtag, setBaseEtag] = useState<string | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [saving, setSaving] = useState(false);
  const area = useRef<HTMLTextAreaElement>(null);
  const statusId = useId();

  // Linhas alteradas pela última atualização vinda do disco (controles ao
  // lado, outra aba, edição do arquivo): marcadas na margem até a próxima.
  const prevText = useRef<string | null>(null);
  const [changed, setChanged] = useState<Set<number>>(new Set());
  useEffect(() => {
    const prev = prevText.current;
    prevText.current = doc.text;
    if (prev === null || prev === doc.text) return;
    setChanged(changedLines(prev.split("\n"), doc.text.split("\n")));
  }, [doc.text]);

  const text = draft ?? doc.text;
  const dirty = draft !== null && draft !== doc.text;
  const moved = dirty && baseEtag !== null && doc.etag !== null && baseEtag !== doc.etag;
  const lines = useMemo(() => text.replace(/\n$/, "").split("\n"), [text]);
  const badLine = error?.body.line ?? null;

  const submit = (force = false) => {
    if (draft === null) return;
    setSaving(true);
    setError(null);
    save(draft, force ? undefined : (baseEtag ?? doc.etag ?? undefined)).then(
      () => {
        setSaving(false);
        setDraft(null);
        setBaseEtag(null);
      },
      (e: unknown) => {
        setSaving(false);
        setError(toApiError(e));
      },
    );
  };

  const discard = () => {
    setDraft(null);
    setBaseEtag(null);
    setError(null);
  };

  const goToLine = (line: number, column = 1) => {
    const el = area.current;
    if (!el) return;
    const all = text.split("\n");
    let pos = 0;
    for (let i = 0; i < line - 1 && i < all.length; i++) pos += all[i]!.length + 1;
    pos += Math.max(0, column - 1);
    el.focus({ preventScroll: true });
    el.setSelectionRange(pos, pos);
    // Rola o painel até a linha, descontando a cabeça fixa do documento.
    const scroller = el.closest(".panel__body");
    const head = el.closest(".docwrap")?.querySelector<HTMLElement>(".dochead");
    const lh = parseFloat(getComputedStyle(el).lineHeight) || 19;
    if (scroller) {
      const top =
        el.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop + (line - 1) * lh;
      scroller.scrollTo({ top: Math.max(0, top - (head?.offsetHeight ?? 0) - 2 * lh) });
    }
  };

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if ((e.ctrlKey || e.metaKey) && (e.key === "s" || e.key === "Enter")) {
      e.preventDefault();
      if (dirty && !saving) submit();
    }
    // Tab insere recuo (YAML não aceita tabulação: dois espaços). Esc sai do
    // campo, para a navegação por teclado nunca ficar presa aqui.
    if (e.key === "Tab" && !e.shiftKey && !e.ctrlKey && !e.altKey && !e.metaKey && e.currentTarget.selectionStart === e.currentTarget.selectionEnd) {
      e.preventDefault();
      const el = e.currentTarget;
      const s = el.selectionStart;
      const next = el.value.slice(0, s) + "  " + el.value.slice(s);
      if (draft === null) setBaseEtag(doc.etag);
      setDraft(next);
      requestAnimationFrame(() => el.setSelectionRange(s + 2, s + 2));
    }
    if (e.key === "Escape") {
      e.preventDefault();
      e.currentTarget.blur();
    }
  };

  return (
    <div className="docwrap">
      <div className="dochead">
      <div className={"docbar" + (dirty ? " docbar--dirty" : "")} id={statusId} role="status">
        {dirty ? (
          <>
            <span className="docbar__state">alterações não gravadas</span>
            <button type="button" className="button button--primary" disabled={saving} onClick={() => submit()}>
              {saving ? "Gravando" : "Gravar"} <kbd>Ctrl S</kbd>
            </button>
            <button type="button" className="button" disabled={saving} onClick={discard}>
              Descartar
            </button>
          </>
        ) : (
          <span className="docbar__state dim">
            {doc.exists ? "igual ao disco" : `${label} não existe; gravar cria o arquivo`}
            {changed.size ? (
              <span className="docbar__changed">
                {" "}
                · {changed.size} {changed.size === 1 ? "linha mudou" : "linhas mudaram"} na última gravação
              </span>
            ) : null}
          </span>
        )}
        <span className="docbar__lang mono dim">{lang}</span>
      </div>

      {notes.length > 0 && !dirty ? (
        <ul className="proc__notes" aria-live="polite">
          {notes.map((n, i) => (
            <li key={i}>{n}</li>
          ))}
        </ul>
      ) : null}
      {hasComments && !dirty ? (
        <p className="notice notice--state">
          Tem comentários. Gravar este texto direto os mantém; os controles ao lado reescrevem o arquivo e os perdem.
        </p>
      ) : null}
      {moved && !error ? (
        <p className="notice notice--state" role="status">
          O arquivo mudou em disco depois que você começou a editar. Gravar agora vai recusar por versão (
          <span className="mono">412 stale</span>) para não apagar a mudança.
        </p>
      ) : null}
      {error ? (
        <div className="docerr">
          <ErrorNote
            error={error}
            what={
              isApiError(error, "stale")
                ? "O documento mudou em disco; nada foi gravado"
                : isApiError(error, "invalid")
                  ? "Documento recusado; nada foi gravado"
                  : `${request} falhou; nada foi gravado`
            }
            onDismiss={() => setError(null)}
          />
          <p className="inline docerr__actions">
            {error.body.line ? (
              <button type="button" className="text-button" onClick={() => goToLine(error.body.line!, error.body.column)}>
                ir para a linha {error.body.line}
              </button>
            ) : null}
            {isApiError(error, "stale") ? (
              <>
                <button type="button" className="text-button" onClick={() => submit(true)}>
                  gravar por cima da versão do disco
                </button>
                <button type="button" className="text-button" onClick={discard}>
                  descartar e ver a versão do disco
                </button>
              </>
            ) : null}
          </p>
        </div>
      ) : null}
      </div>

      <div className="doc">
        <div className="doc__gutter" aria-hidden="true">
          {lines.map((_, i) => (
            <div
              key={i}
              className={
                (badLine === i + 1 ? "is-bad" : "") + (!dirty && changed.has(i) ? " is-changed" : "")
              }
            >
              {i + 1}
            </div>
          ))}
        </div>
        <textarea
          ref={area}
          className="doc__text"
          value={text}
          rows={lines.length}
          wrap="off"
          spellCheck={false}
          autoCapitalize="off"
          autoComplete="off"
          aria-label={`Documento ${label}, editável. Ctrl+S grava, Tab recua, Esc sai do campo.`}
          aria-describedby={statusId}
          aria-invalid={error && isApiError(error, "invalid") ? true : undefined}
          onChange={(e) => {
            if (draft === null) setBaseEtag(doc.etag);
            setDraft(e.target.value);
          }}
          onKeyDown={onKey}
        />
      </div>
    </div>
  );
}
