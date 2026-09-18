import { useEffect, useId, useRef, useState } from "react";
import {
  api,
  type ApiError,
  type EffectiveValue,
  type ReloadResult,
  type SettingsPatchResult,
  type SettingsView,
} from "../api";
import { bytes, shortPath } from "../format";
import { toApiError, useResource } from "../hooks";
import { patchForKey } from "../patch";
import { IntField, Segmented, Switch, TextField } from "./Controls";
import { ErrorNote } from "./ErrorNote";
import { LockIcon } from "./Icons";
import { Failure, Loading } from "./States";

// Configuração do processo (gateway.json), chave a chave, na ordem de
// GET /api/settings. Cada controle grava por PATCH /api/settings, que aplica a
// quente; um valor vindo do ambiente aparece travado com o nome da variável.

const LABELS: Record<string, { label: string; hint?: string }> = {
  "ports.traffic": { label: "porta de tráfego", hint: "reabre o listener; conexões em curso terminam na porta antiga" },
  "ports.admin": { label: "porta de administração", hint: "o painel passa a viver na porta nova" },
  seed: { label: "seed", hint: "vazio: sorteio não reproduzível" },
  "history.backend": {
    label: "backend do histórico",
    hint: "trocar não migra as trocas já gravadas; as setas só escolhem, Espaço ou Enter aplica",
  },
  "history.path": { label: "arquivo do histórico", hint: "para ndjson e sqlite" },
  "history.capacity": { label: "capacidade do histórico", hint: "trocas guardadas" },
  "history.record": { label: "registro do histórico" },
  "history.expose": { label: "exposição do histórico" },
  "capture.maxBodyBytes": { label: "corpo capturado, máximo", hint: "em bytes; o excesso é truncado" },
  "learning.enabled": { label: "modo aprendizado" },
  routesDir: { label: "diretório de rotas" },
};

export function ProcessBody({
  version,
  onSettings,
}: {
  version: number;
  /** Resultado de uma escrita das configurações (a porta de administração pode ter mudado). */
  onSettings?: (r: SettingsPatchResult) => void;
}) {
  // Evento de configuração: relê mantendo os controles à vista. Voltar a
  // "carregando" desmontaria os campos e perderia o que está sendo digitado.
  const [settings, reload] = useResource((s) => api.settings(s), []);
  const firstVersion = useRef(version);
  useEffect(() => {
    if (version !== firstVersion.current) reload();
  }, [version, reload]);
  // A resposta do PATCH já traz a configuração nova; ela vale até a releitura.
  const [latest, setLatest] = useState<SettingsView | null>(null);
  useEffect(() => setLatest(null), [settings]);
  const [notes, setNotes] = useState<string[]>([]);
  const [error, setError] = useState<{ key: string; error: ApiError } | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  if (settings.kind === "loading") return <Loading what="a configuração do processo" />;
  if (settings.kind === "error") {
    return <Failure what="a configuração" request="GET /api/settings" error={settings.error} onRetry={reload} />;
  }
  const view = latest ?? settings.data;

  const commit = (key: string, value: unknown) => {
    setBusy(key);
    setError(null);
    api.patchSettings(patchForKey(key, value)).then(
      (r) => {
        setBusy(null);
        setLatest(r.settings);
        setNotes(r.notes);
        onSettings?.(r);
      },
      (e: unknown) => {
        setBusy(null);
        setError({ key, error: toApiError(e) });
      },
    );
  };

  return (
    <div className="proc">
      <p className="proc__file">
        {view.file.exists ? (
          <>
            Valores efetivos. O que vem de <span className="mono">{view.file.path}</span> se altera aqui e vale na hora.
          </>
        ) : (
          <>
            <span className="mono">{view.file.path}</span> ainda não existe: tudo está no padrão ou no ambiente. A primeira
            alteração cria o arquivo.
          </>
        )}
      </p>
      <div className="settings" role="list">
        {view.values.map((v) => (
          <Setting
            key={v.key}
            v={v}
            busy={busy === v.key}
            error={error?.key === v.key ? error.error : null}
            onDismiss={() => setError(null)}
            onCommit={(value) => commit(v.key, value)}
          />
        ))}
      </div>
      {notes.length ? (
        <ul className="proc__notes" aria-live="polite">
          {notes.map((n, i) => (
            <li key={i}>{n}</li>
          ))}
        </ul>
      ) : null}
      <ReloadFromDisk />
    </div>
  );
}

function Origin({ v }: { v: EffectiveValue }) {
  if (v.locked || v.source.origin === "env") {
    return (
      <span className="origin origin--env" title={`Definido pela variável de ambiente ${v.source.name ?? v.env}, que vence gateway.json. Para mudar, altere a variável e reinicie o processo.`}>
        <LockIcon /> <span className="mono">{v.source.name ?? v.env}</span>
      </span>
    );
  }
  if (v.source.origin === "file") {
    const file = v.source.name ?? "gateway.json";
    return (
      <span className="origin mono" title={file}>
        {shortPath(file)}
      </span>
    );
  }
  return <span className="origin origin--default">padrão</span>;
}

function Setting({
  v,
  busy,
  error,
  onDismiss,
  onCommit,
}: {
  v: EffectiveValue;
  busy: boolean;
  error: ApiError | null;
  onDismiss: () => void;
  onCommit: (value: unknown) => void;
}) {
  const id = useId();
  const meta = LABELS[v.key] ?? { label: v.key };
  const locked = v.locked;
  const hintId = `${id}-h`;
  return (
    <div className={"setting" + (locked ? " setting--locked" : "")} role="listitem">
      <label className="setting__label" htmlFor={id}>
        {meta.label}
        <span className="setting__key mono">{v.key}</span>
      </label>
      <div className="setting__ctl">
        <Control v={v} id={id} locked={locked} onCommit={onCommit} describedBy={hintId} />
        {busy ? <span className="dim">aplicando</span> : null}
      </div>
      <div className="setting__origin">
        <Origin v={v} />
        {v.source.origin === "file" && !locked ? (
          <button
            type="button"
            className="text-button"
            title={`Remove ${v.key} de gateway.json e volta ao valor padrão`}
            onClick={() => onCommit(null)}
          >
            usar o padrão
          </button>
        ) : null}
      </div>
      <p className="setting__hint" id={hintId}>
        {locked ? (
          <>
            Travado por <span className="mono">{v.source.name ?? v.env}</span>. A API recusa alterar; mude a variável e
            reinicie.
          </>
        ) : (
          meta.hint
        )}
      </p>
      {error ? <ErrorNote error={error} onDismiss={onDismiss} what={`${v.key} não foi alterado`} /> : null}
    </div>
  );
}

function Control({
  v,
  id,
  locked,
  onCommit,
  describedBy,
}: {
  v: EffectiveValue;
  id: string;
  locked: boolean;
  onCommit: (value: unknown) => void;
  describedBy: string;
}) {
  const label = LABELS[v.key]?.label ?? v.key;
  if (typeof v.value === "boolean") {
    return (
      <span className="inline">
        <Switch id={id} checked={v.value} label={label} disabled={locked} describedBy={describedBy} onChange={(b) => onCommit(b)} />
        <span className="dim">{v.value ? "ligado" : "desligado"}</span>
      </span>
    );
  }
  if (v.key === "history.backend") {
    const value = String(v.value) as "memory" | "ndjson" | "sqlite";
    return (
      <span id={id}>
        <Segmented
          label={label}
          value={value}
          disabled={locked}
          manual
          onChange={(b) => onCommit(b)}
          options={[
            { value: "memory", label: "memory" },
            { value: "ndjson", label: "ndjson" },
            { value: "sqlite", label: "sqlite" },
          ]}
        />
      </span>
    );
  }
  if (typeof v.value === "number" || v.value === null || v.key === "seed") {
    const isPort = v.key.startsWith("ports.");
    return (
      <span className="inline">
        <IntField
          id={id}
          label={label}
          value={v.value as number | null}
          disabled={locked}
          min={isPort ? 1 : 0}
          max={isPort ? 65535 : undefined}
          placeholder={v.key === "seed" ? "aleatório" : undefined}
          describedBy={describedBy}
          onCommit={(n) => onCommit(n)}
        />
        {v.key === "capture.maxBodyBytes" && typeof v.value === "number" ? (
          <span className="dim mono setting__unit">{bytes(v.value)}</span>
        ) : null}
      </span>
    );
  }
  return (
    <TextField
      id={id}
      label={label}
      mono
      size="sm"
      value={String(v.value ?? "")}
      disabled={locked}
      describedBy={describedBy}
      onCommit={(t) => onCommit(t.trim() === "" ? null : t.trim())}
    />
  );
}

/** Paridade com editar os arquivos fora do painel: POST /api/reload. */
function ReloadFromDisk() {
  const [result, setResult] = useState<ReloadResult | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [busy, setBusy] = useState(false);
  return (
    <div className="proc__reload">
      <button
        type="button"
        className="button"
        disabled={busy}
        onClick={() => {
          setBusy(true);
          setError(null);
          api.reload().then(
            (r) => {
              setBusy(false);
              setResult(r);
            },
            (e: unknown) => {
              setBusy(false);
              setError(toApiError(e));
            },
          );
        }}
      >
        Reler os arquivos do disco
      </button>
      <span className="dim">para quando gateway.json ou routes/ foram editados fora do painel</span>
      {result ? (
        <p className="proc__notes" aria-live="polite">
          {result.routes} rotas carregadas.
          {result.changed.routes.length ? ` Rotas alteradas: ${result.changed.routes.join(", ")}.` : " Nenhuma rota mudou."}
          {result.changed.settings.length ? ` Configurações: ${result.changed.settings.join(", ")}.` : ""}
          {result.warnings.map((w, i) => (
            <span key={i} className="dim">
              {" "}
              {w}.
            </span>
          ))}
        </p>
      ) : null}
      {error ? <ErrorNote error={error} onDismiss={() => setError(null)} what="A releitura falhou; a configuração anterior continua" /> : null}
    </div>
  );
}
