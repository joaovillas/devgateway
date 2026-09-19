import { useCallback, useId, useState, type ReactNode } from "react";
import { api, type Latency, type Override, type OverrideLiveState, type OverrideResource } from "../api";
import { useStale, type OnOverrideState } from "../live";
import type { CommentsGuard, GuardedDoc } from "../commentsGuard";
import { WriteCancelled } from "../commentsGuard";
import { clock, formatDuration, ms, parseDuration, PATH_HINT, rulePathProblem, seconds } from "../format";
import { RuleSelector } from "./PathText";
import { toApiError, useResource } from "../hooks";
import { mergeDiff, type MergePatch } from "../patch";
import { advancedFeatures, advancedNote, type DetailMode } from "../mode";
import { isActive } from "../services";
import { usePatchWriter } from "../writer";
import {
  DurationField,
  DurationSlider,
  IntField,
  ProbabilityControl,
  Row,
  Segmented,
  Switch,
  TextField,
} from "./Controls";
import { ChevronIcon, SlidersIcon } from "./Icons";
import { BodyMatcherEditor, MapEditor } from "./MapEditor";
import { ErrorNote } from "./ErrorNote";
import type { ApiError } from "../api";

interface OverrideItemProps {
  route: string;
  doc: GuardedDoc;
  override: Override;
  state: OverrideLiveState | undefined;
  /** Instante em que `state` chegou, base da contagem regressiva. */
  at: number;
  now: number;
  guard: CommentsGuard;
  /** Destaca a troca no tráfego (a lista a mostra quando ela está carregada). */
  onShowExchange: (id: string) => void;
  /** Estado vivo devolvido pela escrita, aplicado já no painel e no mapa. */
  onState?: OnOverrideState;
  /** Simples mostra liga/desliga, nome, efeito e probabilidade; avançado, tudo. */
  mode: DetailMode;
  /** Leva ao modo avançado a partir da marca de recurso avançado. */
  onAdvanced: () => void;
}

const CONTINUOUS = ["probability", "latency", "drop"];

type LatMode = "none" | "fixed" | "range";

function latMode(l: Latency | undefined): LatMode {
  if (l === undefined) return "none";
  return typeof l === "string" ? "fixed" : "range";
}

/** Um override da rota: liga/desliga, controles contínuos e edição completa. */
export function OverrideItem({
  route,
  doc,
  override,
  state,
  at,
  now,
  guard,
  onShowExchange,
  onState,
  mode,
  onAdvanced,
}: OverrideItemProps) {
  const simple = mode === "simple";
  const stale = useStale();
  const applyState = useCallback(
    (r: OverrideResource) => {
      if (r.state) onState?.(route, r.override.name, r.override.enabled !== false, r.state);
      return r;
    },
    [onState, route],
  );
  const send = useCallback(
    (patch: MergePatch) =>
      guard
        .guard(doc, () => api.patchOverride(route, override.name, patch))
        .then(applyState)
        .then((r) => r.override),
    [guard, doc, route, override.name, applyState],
  );
  const reset = () => act(() => api.resetOverride(route, o.name).then(applyState));
  const w = usePatchWriter<Override>(override, send);
  const o = w.value;
  const enabled = o.enabled !== false;
  const active = isActive(o, state, now, at);
  const ids = useId();

  /**
   * Controle contínuo: num override desligado, o mesmo gesto o liga
   * (a API não liga sozinha; ver PATCH em docs/api.md).
   */
  const adjust = (patch: MergePatch, delay?: number) => {
    const touches = Object.keys(patch).some((k) => CONTINUOUS.includes(k));
    w.change(touches && !enabled ? { ...patch, enabled: true } : patch, delay);
  };

  const [actionError, setActionError] = useState<ApiError | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const act = (run: () => Promise<unknown>) => {
    setActionError(null);
    run().catch((e: unknown) => {
      if (!(e instanceof WriteCancelled)) setActionError(toApiError(e));
    });
  };

  const latencyMode = latMode(o.latency);
  const lat = o.latency;
  const fixed = typeof lat === "string" ? lat : "200ms";
  const range = typeof lat === "object" ? lat : null;
  const ms0 = (d: string) => parseDuration(d) ?? 0;

  const setMode = (m: LatMode) => {
    if (m === latencyMode) return;
    if (m === "none") adjust({ latency: null });
    else if (m === "fixed") adjust({ latency: range ? range.min : "200ms" });
    else {
      const base = typeof lat === "string" ? lat : "100ms";
      adjust({ latency: { min: base, max: formatDuration(Math.max(ms0(base) * 2, 20)) } });
    }
  };

  // O que a regra usa e só o avançado edita: no simples vira a marca do
  // cabeçalho, para nada ficar escondido sem aviso.
  const features = advancedFeatures(o);
  const note = features.length ? advancedNote(features) : null;

  return (
    <li
      className={"ov" + (simple ? " ov--simple" : "") + (active ? " ov--active" : "") + (enabled ? "" : " ov--off")}
      aria-labelledby={`${ids}-name`}
    >
      <div className="ov__head">
        <Switch
          checked={enabled}
          label={`Regra ${o.name}`}
          onChange={(v) => w.change({ enabled: v })}
        />
        <span className="ov__name" id={`${ids}-name`}>
          {o.name}
        </span>
        {o.source ? (
          <span className={`prov prov--${o.source.kind}`} title={o.source.kind === "learned" ? "Criada pelo modo aprendizado a partir de uma troca real" : "Derivada de uma troca capturada"}>
            {o.source.kind === "learned" ? "aprendida" : "derivada"}
          </span>
        ) : null}
        {simple ? (
          <span className="ov__effect">
            <Effect o={o} />
          </span>
        ) : null}
        {simple && note ? (
          <button type="button" className="icon-button ov__adv" title={note} onClick={onAdvanced}>
            <SlidersIcon />
            <span className="sr-only">{`A regra ${o.name} ${note}`}</span>
          </button>
        ) : null}
        <LiveState enabled={enabled} active={active} state={state} now={now} at={at} busy={w.busy ? (guard.asking === doc.file ? "esperando a confirmação" : "gravando") : null} stale={stale} compact={simple} onReset={reset} />
      </div>

      {simple ? null : (
        <p className="ov__sel">
          <span className="mono">
            <RuleSelector o={o} />
          </span>
          <span className="dim"> → </span>
          <Effect o={o} />
        </p>
      )}

      <div className="ov__controls">
        <Row label="probabilidade" htmlFor={`${ids}-p`}>
          <ProbabilityControl
            id={`${ids}-p`}
            label={`Probabilidade de ${o.name}`}
            value={o.probability ?? 1}
            onChange={(p, delay) => adjust({ probability: p }, delay)}
            onSettle={w.flush}
          />
        </Row>
        {simple ? null : (
          <>
          <Row label="latência">
            <div className="lat">
              <Segmented<LatMode>
                label={`Latência de ${o.name}`}
                value={latencyMode}
                onChange={setMode}
                options={[
                  { value: "none", label: "sem" },
                  { value: "fixed", label: "fixa" },
                  { value: "range", label: "intervalo" },
                ]}
              />
              {latencyMode === "fixed" ? (
                <DurationSlider
                  label={`Latência fixa de ${o.name}`}
                  value={fixed}
                  onChange={(d, delay) => adjust({ latency: d }, delay)}
                  onSettle={w.flush}
                />
              ) : null}
              {latencyMode === "range" && range ? (
                <>
                  <span className="lat__bound">
                    <span className="lat__label">mín</span>
                    <DurationSlider
                      label={`Latência mínima de ${o.name}`}
                      onSettle={w.flush}
                      value={range.min}
                      onChange={(d, delay) =>
                        adjust({ latency: { min: d, max: ms0(d) > ms0(range.max) ? d : range.max } }, delay)
                      }
                    />
                  </span>
                  <span className="lat__bound">
                    <span className="lat__label">máx</span>
                    <DurationSlider
                      label={`Latência máxima de ${o.name}`}
                      onSettle={w.flush}
                      value={range.max}
                      onChange={(d, delay) =>
                        adjust({ latency: { min: ms0(d) < ms0(range.min) ? d : range.min, max: d } }, delay)
                      }
                    />
                  </span>
                </>
              ) : null}
            </div>
          </Row>
          <Row label="queda">
            <span className="inline">
              <Switch
                checked={o.drop === true}
                label={`Derrubar a conexão em ${o.name}`}
                onChange={(v) => adjust({ drop: v ? true : null })}
              />
              <span className={o.drop ? "tone-drop" : "dim"}>
                {o.drop ? "derruba a conexão sem responder" : "responde normalmente"}
              </span>
            </span>
          </Row>
          </>
        )}
      </div>

      {o.source ? <SourceLine source={o.source} onShowExchange={onShowExchange} /> : null}

      {w.error ? <ErrorNote error={w.error} onDismiss={w.dismissError} what="A alteração não foi gravada" /> : null}
      {actionError ? <ErrorNote error={actionError} onDismiss={() => setActionError(null)} what="A ação falhou" /> : null}

      {simple ? null : (
        <details className="more">
          <summary>
            <ChevronIcon /> critérios, resposta e limites
          </summary>
          <OverrideForm
            o={o}
            ids={ids}
            change={(p) => w.change(p)}
            rename={(name) =>
              act(() => guard.guard(doc, () => api.replaceOverride(route, override.name, { ...o, name })))
            }
          />
          <div className="more__actions">
            <button type="button" className="text-button" onClick={reset}>
              reiniciar TTL e contagem
            </button>
            {confirmDelete ? (
              <span className="inline">
                <button
                  type="button"
                  className="text-button text-button--danger"
                  autoFocus
                  onClick={() => act(() => guard.guard(doc, () => api.deleteOverride(route, override.name)))}
                >
                  confirmar remoção de {o.name}
                </button>
                <button type="button" className="text-button" onClick={() => setConfirmDelete(false)}>
                  manter
                </button>
              </span>
            ) : (
              <button type="button" className="text-button" onClick={() => setConfirmDelete(true)}>
                remover regra
              </button>
            )}
          </div>
        </details>
      )}
    </li>
  );
}

function Effect({ o }: { o: Override }) {
  const parts = [];
  if (o.drop) parts.push(<span key="d" className="tone-drop">queda</span>);
  else if (o.respond) parts.push(<span key="s" className="tone-drop mono">{o.respond.status ?? 200}</span>);
  if (o.latency) {
    const l = typeof o.latency === "string" ? o.latency : `${o.latency.min}–${o.latency.max}`;
    parts.push(<span key="l" className="tone-injected mono">+{l}</span>);
  }
  // "destino" só quando não há mais nada a dizer: com atraso declarado, o
  // resumo é o atraso, e ir ao destino é o que já se espera.
  if (parts.length === 0) parts.push(<span key="u" className="dim">destino</span>);
  return (
    <>
      {parts.map((p, i) => (
        <span key={i}>
          {i > 0 ? <span className="dim"> · </span> : null}
          {p}
        </span>
      ))}
    </>
  );
}

function LiveState({
  enabled,
  active,
  state,
  now,
  at,
  busy,
  stale,
  compact,
  onReset,
}: {
  enabled: boolean;
  active: boolean;
  state: OverrideLiveState | undefined;
  now: number;
  at: number;
  busy: string | null;
  /** Sem conexão: TTL e aplicações não estão sendo confirmados pelo servidor. */
  stale: boolean;
  /**
   * Modo simples: só o que se esgota (prazo, limite, expiração). "Ativa" e
   * "desligada" o interruptor ao lado já diz, e repetir seria ruído.
   */
  compact: boolean;
  onReset: () => void;
}) {
  const parts: ReactNode[] = [];
  if (!enabled) {
    if (!compact) parts.push(<span key="s">desligada</span>);
  } else if (state && !active) {
    const why =
      state.expired === "applications" || (state.maxApplications !== null && state.applications >= state.maxApplications)
        ? "esgotou as aplicações"
        : "expirou pelo TTL";
    parts.push(
      <span key="s" className="ov__expired">
        {why}{" "}
        <button type="button" className="link-button" onClick={onReset}>
          reiniciar
        </button>
      </span>,
    );
  } else {
    if (!compact) {
      parts.push(
        <span key="s" className="ov__on">
          ativa
        </span>,
      );
    }
    if (state?.ttlRemainingMs != null) {
      parts.push(
        <span key="t" className="mono" title="Tempo de vida restante">
          {seconds(state.ttlRemainingMs - (now - at))}
        </span>,
      );
    }
  }
  if (state && (state.maxApplications !== null || (!compact && state.applications > 0))) {
    parts.push(
      <span key="a" className="mono" title="Aplicações desde o início do relógio">
        {state.applications}
        {state.maxApplications !== null ? `/${state.maxApplications}` : ""} aplic.
      </span>,
    );
  }
  if (stale && enabled && state && (!compact || parts.length > 0)) {
    parts.push(
      <span key="p" title="Sem conexão com /api/events: estes números são da última atualização e não estão sendo confirmados">
        parado
      </span>,
    );
  }
  return (
    <span className={"ov__live" + (stale ? " is-stale" : "")}>
      {busy ? <span className="ov__busy">{busy}</span> : null}
      {parts.map((p, i) => (
        <span key={i} className="ov__live-part">
          {p}
        </span>
      ))}
    </span>
  );
}

function SourceLine({
  source,
  onShowExchange,
}: {
  source: NonNullable<Override["source"]>;
  onShowExchange: (id: string) => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className="src">
      <p className="src__line">
        {source.kind === "learned" ? "aprendida" : "derivada"} da troca{" "}
        <span className="mono" title={source.exchange}>
          …{source.exchange.slice(-8)}
        </span>
        {source.at ? <span className="dim"> em {clock(source.at)}</span> : null}
        {source.bodyIncomplete ? <span className="tone-injected"> · corpo incompleto na captura</span> : null}{" "}
        <button type="button" className="link-button" aria-expanded={open} onClick={() => setOpen((v) => !v)}>
          {open ? "fechar a troca de origem" : "abrir a troca de origem"}
        </button>
      </p>
      {open ? <ExchangePeek id={source.exchange} onShowExchange={onShowExchange} /> : null}
    </div>
  );
}

/** A troca que originou a regra (override), lida do histórico se ainda estiver lá. */
function ExchangePeek({ id, onShowExchange }: { id: string; onShowExchange: (id: string) => void }) {
  const [ex] = useResource((s) => api.getExchange(id, s), [id]);
  if (ex.kind === "loading") return <p className="src__peek dim">lendo a troca…</p>;
  if (ex.kind === "error") {
    const e = ex.error;
    const text =
      e.code === "not_found"
        ? "A troca de origem já saiu do histórico (o backend guarda um número limitado de trocas)."
        : e.code === "history_disabled"
          ? "O histórico está desabilitado (history.expose), então a troca de origem não pode ser lida."
          : `Não foi possível ler a troca: ${e.message}`;
    return <p className="src__peek dim">{text}</p>;
  }
  const x = ex.data;
  const ct = (h: Record<string, string[]> | undefined) => h?.["Content-Type"]?.[0] ?? h?.["content-type"]?.[0];
  return (
    <div className="src__peek">
      <p className="mono">
        {x.method} {x.path}
        {x.query ? "?" + x.query : ""} <span className="dim">→</span> {x.status || "sem resposta"}{" "}
        <span className="dim">
          · {ms(x.timing.totalMs)} · {clock(x.start)}
        </span>
      </p>
      <p className="dim">
        requisição {x.request.size} B{ct(x.request.headers) ? ` (${ct(x.request.headers)})` : ""} · resposta{" "}
        {x.response.size} B{ct(x.response.headers) ? ` (${ct(x.response.headers)})` : ""}
        {x.response.truncated ? ", truncada na captura" : ""}
      </p>
      <p>
        <button type="button" className="link-button" onClick={() => onShowExchange(x.id)}>
          abrir a troca completa
        </button>
      </p>
    </div>
  );
}

/** Todos os campos do override, com gravação ao sair de cada campo. */
function OverrideForm({
  o,
  ids,
  change,
  rename,
}: {
  o: Override;
  ids: string;
  change: (p: MergePatch) => void;
  rename: (name: string) => void;
}) {
  const m = o.match;
  const r = o.respond;
  return (
    <div className="form">
      <Row label="nome" htmlFor={`${ids}-name-in`}>
        <TextField
          id={`${ids}-name-in`}
          label="Nome da regra"
          mono
          size="sm"
          value={o.name}
          validate={(t) => (t.trim() ? null : "o nome é obrigatório")}
          onCommit={(t) => rename(t.trim())}
        />
      </Row>

      <h4 className="form__group">critérios</h4>
      <Row label="path" htmlFor={`${ids}-path`} hint={PATH_HINT}>
        <TextField
          id={`${ids}-path`}
          label="Path"
          mono
          size="sm"
          value={m.path ?? ""}
          placeholder="qualquer"
          validate={rulePathProblem}
          onCommit={(t) => change({ match: { path: t.trim() || null } })}
        />
      </Row>
      <Row label="path regex" htmlFor={`${ids}-rx`}>
        <TextField
          id={`${ids}-rx`}
          label="Path por regex"
          mono
          size="sm"
          value={m.pathRegex ?? ""}
          placeholder="nenhuma"
          validate={(t) => {
            if (!t.trim()) return null;
            try {
              new RegExp(t);
              return null;
            } catch {
              return "regex inválida";
            }
          }}
          onCommit={(t) => change({ match: { pathRegex: t.trim() || null } })}
        />
      </Row>
      <Row label="método" htmlFor={`${ids}-method`}>
        <TextField
          id={`${ids}-method`}
          label="Método"
          mono
          size="sm"
          list="http-methods"
          value={m.method ?? ""}
          placeholder="qualquer"
          onCommit={(t) => change({ match: { method: t.trim().toUpperCase() || null } })}
        />
      </Row>
      <Row label="cabeçalhos">
        <MapEditor
          label="Critérios de cabeçalho"
          keyPlaceholder="X-Tenant"
          entries={m.headers}
          onCommit={(next) => change({ match: { headers: next ? replaceWith(m.headers, next) : null } })}
        />
      </Row>
      <Row label="query">
        <MapEditor
          label="Critérios de query"
          keyPlaceholder="retry"
          entries={m.query}
          onCommit={(next) => change({ match: { query: next ? replaceWith(m.query, next) : null } })}
        />
      </Row>
      <Row label="corpo">
        <BodyMatcherEditor
          label="Critério do corpo"
          value={m.body}
          onCommit={(next) => change({ match: { body: next === null ? null : replaceWith(m.body, next) } })}
        />
      </Row>

      <h4 className="form__group">resposta</h4>
      <Row label="sintetizar">
        <span className="inline">
          <Switch
            checked={r !== undefined}
            label="Sintetizar resposta"
            onChange={(v) => change({ respond: v ? { status: 503 } : null })}
          />
          <span className="dim">{r ? "o gateway responde no lugar do destino" : "redireciona ao destino"}</span>
        </span>
      </Row>
      {r ? (
        <>
          <Row label="status" htmlFor={`${ids}-status`}>
            <IntField
              id={`${ids}-status`}
              label="Status"
              value={r.status}
              placeholder="200"
              min={100}
              max={599}
              onCommit={(n) => change({ respond: { status: n } })}
            />
          </Row>
          <Row label="cabeçalhos">
            <MapEditor
              plain
              label="Cabeçalhos da resposta"
              keyPlaceholder="Retry-After"
              entries={r.headers}
              onCommit={(next) =>
                change({ respond: { headers: next ? replaceWith(r.headers, next) : null } })
              }
            />
          </Row>
          <Row label="corpo" htmlFor={`${ids}-body`} hint="JSON válido vira estrutura; o resto vai como texto">
            <BodyText id={`${ids}-body`} value={r.body} onCommit={(b) => change({ respond: { body: b === null ? null : replaceWith(r.body, b) } })} />
          </Row>
        </>
      ) : null}

      <h4 className="form__group">limites</h4>
      <Row label="TTL" htmlFor={`${ids}-ttl`} hint="vazio: sem prazo">
        <DurationField id={`${ids}-ttl`} label="Tempo de vida" value={o.ttl} placeholder="sem prazo" onCommit={(d) => change({ ttl: d })} />
      </Row>
      <Row label="máx. aplicações" htmlFor={`${ids}-max`} hint="vazio: sem limite">
        <IntField
          id={`${ids}-max`}
          label="Limite de aplicações"
          value={o.maxApplications}
          placeholder="sem limite"
          min={1}
          onCommit={(n) => change({ maxApplications: n })}
        />
      </Row>

      {o.source ? (
        <>
          <h4 className="form__group">origem</h4>
          <Row label="source">
            <span className="inline">
              <span className="mono dim">
                {o.source.kind} · {o.source.exchange}
              </span>
              <button type="button" className="text-button" onClick={() => change({ source: null })}>
                tratar como declarado
              </button>
            </span>
          </Row>
        </>
      ) : null}
    </div>
  );
}

/**
 * Patch que troca um valor inteiro: entre dois objetos, a diferença com null
 * nas chaves que sumiram (senão o merge patch somaria o antigo ao novo);
 * em qualquer outro caso, o valor novo.
 */
function replaceWith(prev: unknown, next: unknown): unknown {
  const obj = (v: unknown) => typeof v === "object" && v !== null && !Array.isArray(v);
  return obj(prev) && obj(next) ? mergeDiff(prev, next) : next;
}

function BodyText({ id, value, onCommit }: { id: string; value: unknown; onCommit: (b: unknown) => void }) {
  const text = value === undefined ? "" : typeof value === "string" ? value : JSON.stringify(value, null, 2);
  const [draft, setDraft] = useState<string | null>(null);
  const shown = draft ?? text;
  return (
    <textarea
      id={id}
      className="input input--mono textarea"
      value={shown}
      rows={Math.min(8, Math.max(2, shown.split("\n").length))}
      spellCheck={false}
      placeholder="sem corpo"
      onChange={(e) => setDraft(e.target.value)}
      onBlur={() => {
        if (draft === null || draft === text) {
          setDraft(null);
          return;
        }
        let next: unknown = draft;
        if (draft.trim() === "") next = null;
        else {
          try {
            next = JSON.parse(draft) as unknown;
          } catch {
            next = draft;
          }
        }
        setDraft(null);
        onCommit(next);
      }}
    />
  );
}
