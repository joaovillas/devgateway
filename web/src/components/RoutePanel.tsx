import { useCallback, useEffect, useId, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import {
  api,
  type ExchangeFilter,
  type LearningView,
  type Route,
  type RouteResource,
  type SettingsPatchResult,
  type UpstreamHealth,
} from "../api";
import { clock, shortPath } from "../format";
import type { OnOverrideState } from "../live";
import { useCommentsGuard, WriteCancelled, type CommentsGuard, type GuardedDoc } from "../commentsGuard";
import type { Load } from "../hooks";
import { toApiError, useNow } from "../hooks";
import type { MergePatch } from "../patch";
import type { Selection } from "../selection";
import { destinationText, knownDestinations } from "../services";
import { usePatchWriter } from "../writer";
import { DurationField, Row, Switch, TextField } from "./Controls";
import { ErrorNote } from "./ErrorNote";
import { PlusIcon } from "./Icons";
import { OverrideItem } from "./OverrideItem";
import { ProcessBody } from "./ProcessPanel";
import { ExchangeDetail, type OpenedExchange } from "./ExchangeDetail";
import { Empty } from "./States";
import type { ApiError } from "../api";

export type ControlView = "route" | "process" | "exchange";

interface RoutePanelProps {
  view: ControlView;
  onView: (v: ControlView) => void;
  routes: Load<RouteResource[]>;
  selection: Selection;
  onSelect: (s: Selection) => void;
  onRetry: () => void;
  learning: Load<LearningView>;
  /** Abre uma troca no detalhe (a origem de um override aprendido ou derivado). */
  onShowExchange: (id: string) => void;
  /** Muda a cada evento de configuração: a visão do processo relê as configurações. */
  version: number;
  /** Troca aberta no detalhe (aba "troca"), ou null. */
  exchange: OpenedExchange | null;
  onOpenExchange: (ex: OpenedExchange) => void;
  onCloseExchange: () => void;
  /** Filtro ativo do tráfego, que a navegação item a item respeita. */
  exchangeFilter: ExchangeFilter;
  /** Saúde dos upstreams, mostrada quando um upstream está selecionado. */
  upstreams: Load<UpstreamHealth[]>;
  onSettings: (r: SettingsPatchResult) => void;
  onOverrideState: OnOverrideState;
  /** Identifica o projeto nas confirmações de comentários lembradas (diretório de rotas). */
  guardScope: string;
  /** Cadastro de serviço aberto: ocupa a aba do serviço mesmo com um selecionado. */
  creating: boolean;
  onCreate: () => void;
  onCreated: (name: string) => void;
  onCancelCreate: () => void;
}

/**
 * Painel de detalhe, na coluna direita: o serviço selecionado na lista
 * (regras e campos da rota) ou o cadastro de um novo, o processo
 * (gateway.json) ou a troca aberta no tráfego.
 */
export function RoutePanel(props: RoutePanelProps) {
  const { view, onView, routes, selection, exchange } = props;
  const guard = useCommentsGuard(props.guardScope);
  const selected = selection?.kind === "route" ? selection.name : null;
  const res = routes.kind === "ready" && selected ? routes.data.find((r) => r.route.name === selected) : undefined;
  const tabsId = useId();
  const creating = props.creating && view === "route";
  // Serviço recém-criado: até a releitura da lista trazê-lo, ele está chegando, não sumiu.
  const [justCreated, setJustCreated] = useState<string | null>(null);
  const onCreated = props.onCreated;
  const created = useCallback(
    (name: string) => {
      setJustCreated(name);
      onCreated(name);
    },
    [onCreated],
  );

  return (
    <section className="panel" aria-label="Detalhe">
      <header className="panel__head">
        <Tabs id={tabsId} view={view} onView={onView} hasExchange={exchange !== null} />
        {creating ? <span className="panel__sub">cadastro</span> : null}
        {view === "route" && !creating && res ? <span className="panel__sub">{res.route.name}</span> : null}
        {view === "process" ? <span className="panel__sub mono">gateway.json</span> : null}
        {view === "exchange" && exchange ? (
          <span className="panel__sub mono" title={exchange.id}>
            …{exchange.id.slice(-8)}
          </span>
        ) : null}
      </header>
      <div className="panel__body" id={`${tabsId}-panel`} role="tabpanel" aria-labelledby={`${tabsId}-${view}`}>
        {guard.asking ? <CommentsPrompt guard={guard} /> : null}
        {view === "process" ? (
          <ProcessBody version={props.version} onSettings={props.onSettings} />
        ) : creating ? (
          <NewServiceForm
            routes={routes}
            upstreams={props.upstreams}
            onCreated={created}
            onCancel={props.onCancelCreate}
          />
        ) : view === "exchange" && exchange ? (
          <ExchangeDetail
            opened={exchange}
            filter={props.exchangeFilter}
            onOpen={props.onOpenExchange}
            onClose={props.onCloseExchange}
            routes={routes}
            guard={guard}
            onShowRoute={(name) => props.onSelect({ kind: "route", name })}
          />
        ) : (
          <RouteBody {...props} guard={guard} res={res} justCreated={justCreated} />
        )}
      </div>
    </section>
  );
}

function Tabs({
  id,
  view,
  onView,
  hasExchange,
}: {
  id: string;
  view: ControlView;
  onView: (v: ControlView) => void;
  hasExchange: boolean;
}) {
  const refs = useRef<Record<ControlView, HTMLButtonElement | null>>({ route: null, process: null, exchange: null });
  const tabs: { v: ControlView; label: string }[] = [
    { v: "route", label: "serviço" },
    { v: "process", label: "processo" },
    ...(hasExchange ? [{ v: "exchange" as const, label: "troca" }] : []),
  ];
  const onKey = (e: KeyboardEvent) => {
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
    e.preventDefault();
    const i = tabs.findIndex((t) => t.v === view);
    const next = tabs[(i + (e.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length]!.v;
    onView(next);
    refs.current[next]?.focus();
  };
  return (
    <div className="tabs" role="tablist" aria-label="Detalhe">
      {tabs.map((t) => (
        <button
          key={t.v}
          ref={(el) => {
            refs.current[t.v] = el;
          }}
          id={`${id}-${t.v}`}
          type="button"
          role="tab"
          aria-selected={view === t.v}
          aria-controls={`${id}-panel`}
          tabIndex={view === t.v ? 0 : -1}
          className={"tab" + (view === t.v ? " tab--on" : "")}
          onClick={() => onView(t.v)}
          onKeyDown={onKey}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}

/** A confirmação única antes de reescrever um documento com comentários. */
function CommentsPrompt({ guard }: { guard: CommentsGuard }) {
  const titleId = useId();
  const confirmRef = useRef<HTMLButtonElement>(null);
  const returnTo = useRef<Element | null>(null);
  useEffect(() => {
    returnTo.current = document.activeElement;
    confirmRef.current?.focus();
    return () => {
      const el = returnTo.current;
      if (el instanceof HTMLElement && el.isConnected) el.focus();
    };
  }, [guard.asking]);
  return (
    <div
      className="guard"
      role="alertdialog"
      aria-labelledby={titleId}
      aria-describedby={`${titleId}-d`}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          guard.cancel();
        }
      }}
    >
      <p className="guard__title" id={titleId}>
        <span className="mono" title={guard.asking ?? undefined}>{shortPath(guard.asking ?? "")}</span> tem comentários
      </p>
      <p id={`${titleId}-d`}>
        Gravar pelo painel reescreve o arquivo inteiro: os comentários e a ordem das chaves se perdem. Esta pergunta
        aparece uma vez por arquivo. Para manter os comentários, edite o arquivo no seu editor e recarregue.
      </p>
      <p className="guard__actions">
        <button ref={confirmRef} type="button" className="button button--primary" onClick={guard.confirm}>
          Gravar e perder os comentários
        </button>
        <button type="button" className="button" onClick={guard.cancel}>
          Cancelar a alteração
        </button>
      </p>
    </div>
  );
}

function RouteBody({
  routes,
  selection,
  onSelect,
  onRetry,
  learning,
  onShowExchange,
  onOverrideState,
  upstreams,
  onCreate,
  guard,
  res,
  justCreated,
}: RoutePanelProps & { guard: CommentsGuard; res: RouteResource | undefined; justCreated: string | null }) {
  if (routes.kind === "loading") return <Empty title="Carregando os serviços" />;
  if (routes.kind === "error") {
    return (
      <Empty title="Serviços indisponíveis">
        <p>
          <span className="mono">GET /api/routes</span> falhou ({routes.error.code}). O detalhe está na lista de
          serviços.{" "}
          <button type="button" className="link-button" onClick={onRetry}>
            Tentar de novo
          </button>
        </p>
      </Empty>
    );
  }
  if (selection?.kind === "upstream") {
    const users = routes.data.filter((r) => r.route.upstream === selection.name);
    const health = upstreams.kind === "ready" ? upstreams.data.find((u) => u.upstream === selection.name) : undefined;
    return (
      <Empty title="Filtro por destino">
        <UpstreamHealthBlock health={health} load={upstreams} />
        <p>
          O tráfego mostra só o que foi para <span className="mono">{selection.name}</span>. As regras moram nos
          serviços; escolha um dos que usam este destino:
        </p>
        <p className="inline inline--wrap">
          {users.map((r) => (
            <button
              key={r.route.name}
              type="button"
              className="text-button mono"
              onClick={() => onSelect({ kind: "route", name: r.route.name })}
            >
              {r.route.name}
            </button>
          ))}
        </p>
      </Empty>
    );
  }
  if (selection?.kind !== "route") {
    return (
      <div className="state">
        <p className="state__title">Nenhum serviço selecionado</p>
        <p>Selecione um serviço na lista para ajustar as regras dele, ou cadastre um novo.</p>
        <p>
          <button type="button" className="text-button" onClick={onCreate}>
            <PlusIcon /> serviço
          </button>
        </p>
      </div>
    );
  }
  if (!res && justCreated === selection.name) return <Empty title={`Abrindo o serviço ${selection.name}`} />;
  if (!res) {
    return (
      <Empty title={`O serviço ${selection.name} não existe mais`}>
        <p>Ele foi removido ou renomeado desde que foi selecionado.</p>
      </Empty>
    );
  }
  const learned = learning.kind === "ready" ? (learning.data.learned[res.route.name] ?? 0) : 0;
  return (
    <RouteDetail
      key={res.route.name}
      res={res}
      guard={guard}
      learned={learned}
      onShowExchange={onShowExchange}
      onOverrideState={onOverrideState}
      onRenamed={(name) => onSelect({ kind: "route", name })}
      onDeleted={() => onSelect(null)}
    />
  );
}

function RouteDetail({
  res,
  guard,
  learned,
  onShowExchange,
  onOverrideState,
  onRenamed,
  onDeleted,
}: {
  res: RouteResource;
  guard: CommentsGuard;
  learned: number;
  onShowExchange: (id: string) => void;
  onOverrideState: OnOverrideState;
  onRenamed: (name: string) => void;
  onDeleted: () => void;
}) {
  const r = res.route;
  const overrides = r.overrides ?? [];
  const doc: GuardedDoc = useMemo(() => ({ file: res.file, hasComments: res.hasComments }), [res.file, res.hasComments]);
  // Documento reescrito sem comentários: a confirmação antiga deixa de valer.
  useEffect(() => guard.observe(doc), [guard, doc]);
  // Instante em que este estado vivo chegou: base da contagem regressiva do TTL.
  const at = useMemo(() => Date.now(), [res]);
  const ticking = overrides.some((o) => res.state[o.name]?.ttlRemainingMs != null);
  const now = useNow(ticking);
  const [adding, setAdding] = useState(false);

  return (
    <div className="route">
      <div className="route__section">
        <h3 className="section-title">
          regras
          {overrides.length ? <span className="section-title__count mono">{overrides.length}</span> : null}
          {learned ? (
            <span className="section-title__note">
              {learned} {learned === 1 ? "regra aprendida" : "regras aprendidas"}
            </span>
          ) : null}
        </h3>
        {overrides.length === 0 ? (
          <p className="dim">
            {r.upstream
              ? "Nenhuma regra: o gateway redireciona tudo ao destino."
              : "Nenhuma regra e nenhum destino: o gateway responde 501 às requisições deste serviço."}
          </p>
        ) : (
          <ul className="ovs">
            {overrides.map((o) => (
              <OverrideItem
                key={o.name}
                route={r.name}
                doc={doc}
                override={o}
                state={res.state[o.name]}
                at={at}
                now={now}
                guard={guard}
                onShowExchange={onShowExchange}
                onState={onOverrideState}
              />
            ))}
          </ul>
        )}
        {learned > 1 ? (
          <p className="hint">
            As aprendidas casam paths exatos. Para cobrir vários de uma vez, crie uma regra com curinga de
            sufixo (por exemplo <span className="mono">/users/*</span>) e remova as aprendidas.
          </p>
        ) : null}
        {adding ? (
          <NewOverrideForm route={r} doc={doc} guard={guard} onDone={() => setAdding(false)} />
        ) : (
          <button type="button" className="text-button" onClick={() => setAdding(true)}>
            <PlusIcon /> regra
          </button>
        )}
      </div>

      <RouteFields res={res} doc={doc} guard={guard} onRenamed={onRenamed} onDeleted={onDeleted} />
    </div>
  );
}

/** Saúde do destino selecionado: o que na linha da lista só cabe na dica. */
function UpstreamHealthBlock({ health, load }: { health: UpstreamHealth | undefined; load: Load<UpstreamHealth[]> }) {
  if (load.kind === "loading") return <p className="dim">Lendo o estado do destino.</p>;
  if (load.kind === "error") {
    return (
      <p className="dim">
        Saúde desconhecida: <span className="mono">GET /api/upstreams</span> respondeu{" "}
        <span className="mono">{load.error.code}</span>.
      </p>
    );
  }
  if (!health) return <p className="dim">Nenhuma requisição encaminhada a este destino ainda.</p>;
  const state =
    health.status === "down" ? "fora do ar" : health.status === "up" ? "no ar, respondendo" : "sem tentativas recentes";
  return (
    <dl className="uph">
      <dt>estado</dt>
      <dd className={health.status === "down" ? "is-fault" : undefined}>{state}</dd>
      <dt>recentes</dt>
      <dd className="mono">
        {health.recent.failures} {health.recent.failures === 1 ? "falha" : "falhas"} em {health.recent.attempts}{" "}
        {health.recent.attempts === 1 ? "tentativa" : "tentativas"}
      </dd>
      {health.lastError ? (
        <>
          <dt>último erro</dt>
          <dd className="mono">{health.lastError}</dd>
        </>
      ) : null}
      {health.lastFailureAt ? (
        <>
          <dt>última falha</dt>
          <dd className="mono">{clock(health.lastFailureAt)}</dd>
        </>
      ) : null}
      {health.lastSuccessAt ? (
        <>
          <dt>último sucesso</dt>
          <dd className="mono">{clock(health.lastSuccessAt)}</dd>
        </>
      ) : null}
    </dl>
  );
}

type RouteFieldsValue = Omit<Route, "overrides">;

function strip(r: Route): RouteFieldsValue {
  const { overrides: _o, ...rest } = r;
  return rest;
}

/** Os campos da própria rota, com gravação por PATCH ao sair de cada campo. */
function RouteFields({
  res,
  doc,
  guard,
  onRenamed,
  onDeleted,
}: {
  res: RouteResource;
  doc: GuardedDoc;
  guard: CommentsGuard;
  onRenamed: (name: string) => void;
  onDeleted: () => void;
}) {
  const name = res.route.name;
  const base = useMemo(() => strip(res.route), [res.route]);
  const send = useCallback(
    (patch: MergePatch) => guard.guard(doc, () => api.patchRoute(name, patch)).then((x) => strip(x.route)),
    [guard, doc, name],
  );
  const w = usePatchWriter<RouteFieldsValue>(base, send);
  const v = w.value;
  const ids = useId();
  const [actionError, setActionError] = useState<ApiError | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const act = (run: () => Promise<unknown>, then?: () => void) => {
    setActionError(null);
    run().then(then, (e: unknown) => {
      if (!(e instanceof WriteCancelled)) setActionError(toApiError(e));
    });
  };

  const text = (key: "upstream", t: string) => w.change({ [key]: t.trim() || null });

  return (
    <div className="route__section">
      <h3 className="section-title">
        serviço
        <span className="section-title__note">
          {res.order + 1}º em precedência · <span className="mono" title={res.file}>{shortPath(res.file)}</span>
          {w.busy ? " · gravando" : ""}
        </span>
      </h3>
      <div className="form">
        <Row label="nome" htmlFor={`${ids}-name`} hint="renomeia o serviço; o arquivo continua o mesmo">
          <TextField
            id={`${ids}-name`}
            label="Nome do serviço"
            mono
            size="sm"
            value={v.name}
            validate={(t) => (t.trim() ? null : "o nome é obrigatório")}
            onCommit={(t) =>
              act(
                () => guard.guard(doc, () => api.replaceRoute(name, { ...res.route, name: t.trim() })),
                () => onRenamed(t.trim()),
              )
            }
          />
        </Row>
        <h4 className="form__group">entrada</h4>
        <Row label="host" htmlFor={`${ids}-host`} hint="o Host que o seu app chama; vazio aceita qualquer um">
          <TextField
            id={`${ids}-host`}
            label="Host de entrada"
            mono
            size="sm"
            value={v.match.host ?? ""}
            placeholder="qualquer"
            onCommit={(t) => w.change({ match: { host: t.trim() || null } })}
          />
        </Row>
        <Row label="path" htmlFor={`${ids}-path`} hint="exato, ou curinga de sufixo: /api/x/*">
          <TextField
            id={`${ids}-path`}
            label="Path de entrada"
            mono
            size="sm"
            value={v.match.path ?? ""}
            placeholder="qualquer"
            onCommit={(t) => w.change({ match: { path: t.trim() || null } })}
          />
        </Row>
        <h4 className="form__group">destino</h4>
        <Row
          label="url"
          htmlFor={`${ids}-up`}
          hint={v.upstream ? "para onde o gateway redireciona" : "sem destino, só as regras respondem"}
        >
          <TextField
            id={`${ids}-up`}
            label="Destino"
            mono
            size="sm"
            value={v.upstream ?? ""}
            placeholder="http://localhost:9001"
            validate={(t) => (t.trim() === "" || /^https?:\/\/.+/.test(t.trim()) ? null : "URL http:// ou https://")}
            onCommit={(t) => text("upstream", t)}
          />
        </Row>
        <Row label="remove prefixo">
          <span className="inline">
            <Switch
              checked={v.stripPrefix === true}
              label="Remover o prefixo do path"
              onChange={(b) => w.change({ stripPrefix: b ? true : null })}
            />
            <span className="dim">{v.stripPrefix ? "tira o prefixo da entrada antes de redirecionar" : "redireciona o path inteiro"}</span>
          </span>
        </Row>
        <Row label="reescreve host">
          <span className="inline">
            <Switch
              checked={v.rewriteHost === true}
              label="Reescrever o Host para o do destino"
              onChange={(b) => w.change({ rewriteHost: b ? true : null })}
            />
            <span className="dim">{v.rewriteHost ? "usa o Host do destino" : "repassa o Host que o seu app mandou"}</span>
          </span>
        </Row>
        <Row label="timeout" htmlFor={`${ids}-to`} hint="vazio: sem limite próprio">
          <DurationField
            id={`${ids}-to`}
            label="Timeout"
            value={v.timeout}
            placeholder="sem limite"
            onCommit={(d) => w.change({ timeout: d })}
          />
        </Row>
      </div>
      {w.error ? <ErrorNote error={w.error} onDismiss={w.dismissError} what="A alteração não foi gravada" /> : null}
      {actionError ? <ErrorNote error={actionError} onDismiss={() => setActionError(null)} what="A ação falhou" /> : null}
      <p className="route__danger">
        {confirmDelete ? (
          <span className="inline">
            <button
              type="button"
              className="text-button text-button--danger"
              autoFocus
              onClick={() => act(() => api.deleteRoute(name), onDeleted)}
            >
              apagar {shortPath(res.file)} e o serviço {name}
            </button>
            <button type="button" className="text-button" onClick={() => setConfirmDelete(false)}>
              manter
            </button>
          </span>
        ) : (
          <button type="button" className="text-button" onClick={() => setConfirmDelete(true)}>
            remover serviço
          </button>
        )}
      </p>
    </div>
  );
}

/** Regra (override) nova: nasce desligada, e o primeiro ajuste contínuo a liga. */
function NewOverrideForm({
  route,
  doc,
  guard,
  onDone,
}: {
  route: Route;
  doc: GuardedDoc;
  guard: CommentsGuard;
  onDone: () => void;
}) {
  const ids = useId();
  const [name, setName] = useState("");
  const [path, setPath] = useState(route.match.path ?? "");
  const [method, setMethod] = useState("");
  const [status, setStatus] = useState("503");
  const [error, setError] = useState<ApiError | null>(null);
  const [sending, setSending] = useState(false);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setSending(true);
    setError(null);
    guard
      .guard(doc, () =>
        api.createOverride(route.name, {
          name: name.trim(),
          enabled: false,
          match: { ...(path.trim() ? { path: path.trim() } : {}), ...(method.trim() ? { method: method.trim().toUpperCase() } : {}) },
          respond: { status: Number(status) || 503 },
        }),
      )
      .then(onDone, (err: unknown) => {
        setSending(false);
        if (!(err instanceof WriteCancelled)) setError(toApiError(err));
      });
  };

  return (
    <form className="create" onSubmit={submit} aria-labelledby={`${ids}-t`}>
      <p className="create__title" id={`${ids}-t`}>
        Nova regra em <span className="mono">{route.name}</span>
      </p>
      <div className="form">
        <Row label="nome" htmlFor={`${ids}-n`}>
          <input id={`${ids}-n`} className="input input--mono input--sm" required autoFocus value={name} onChange={(e) => setName(e.target.value)} spellCheck={false} />
        </Row>
        <Row label="path" htmlFor={`${ids}-p`}>
          <input id={`${ids}-p`} className="input input--mono input--sm" value={path} placeholder="qualquer" onChange={(e) => setPath(e.target.value)} spellCheck={false} />
        </Row>
        <Row label="método" htmlFor={`${ids}-m`}>
          <input id={`${ids}-m`} className="input input--mono input--sm" list="http-methods" value={method} placeholder="qualquer" onChange={(e) => setMethod(e.target.value)} spellCheck={false} />
        </Row>
        <Row label="responde" htmlFor={`${ids}-s`}>
          <input id={`${ids}-s`} className="input input--mono input--sm" inputMode="numeric" pattern="[1-5][0-9]{2}" value={status} onChange={(e) => setStatus(e.target.value)} />
        </Row>
      </div>
      <p className="hint">Ela nasce desligada. Ajustar a probabilidade, a latência ou a queda a liga.</p>
      {error ? <ErrorNote error={error} what="A regra não foi criada" /> : null}
      <p className="inline">
        <button type="submit" className="button button--primary" disabled={sending || !name.trim()}>
          Criar regra
        </button>
        <button type="button" className="button" onClick={onDone}>
          Cancelar
        </button>
      </p>
    </form>
  );
}

/** Quantos destinos conhecidos viram atalho de um clique no cadastro; o resto fica nas sugestões do campo. */
const QUICK_DESTINATIONS = 6;

/**
 * Cadastro de serviço: nome, entrada e destino. Abre pelo "+ serviço" da
 * lista com ou sem um serviço selecionado; as regras vêm depois, no serviço.
 */
function NewServiceForm({
  routes,
  upstreams,
  onCreated,
  onCancel,
}: {
  routes: Load<RouteResource[]>;
  upstreams: Load<UpstreamHealth[]>;
  onCreated: (name: string) => void;
  onCancel: () => void;
}) {
  const ids = useId();
  const [name, setName] = useState("");
  const [path, setPath] = useState("");
  const [host, setHost] = useState("");
  const [showHost, setShowHost] = useState(false);
  const [dest, setDest] = useState("");
  const [error, setError] = useState<ApiError | null>(null);
  const [sending, setSending] = useState(false);
  const known = useMemo(
    () =>
      knownDestinations(
        routes.kind === "ready" ? routes.data : [],
        upstreams.kind === "ready" ? upstreams.data : [],
      ),
    [routes, upstreams],
  );

  const n = name.trim();
  const taken = routes.kind === "ready" && routes.data.some((r) => r.route.name === n);
  const d = dest.trim();
  const destBad = d !== "" && !/^https?:\/\/.+/.test(d);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!n || taken || destBad) return;
    setSending(true);
    setError(null);
    api
      .createRoute({
        name: n,
        match: { ...(host.trim() ? { host: host.trim() } : {}), ...(path.trim() ? { path: path.trim() } : {}) },
        ...(d ? { upstream: d } : {}),
      })
      .then(
        () => onCreated(n),
        (err: unknown) => {
          setSending(false);
          setError(toApiError(err));
        },
      );
  };

  return (
    <div className="newsvc">
      <form
        className="create"
        onSubmit={submit}
        aria-labelledby={`${ids}-t`}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.preventDefault();
            onCancel();
          }
        }}
      >
        <p className="create__title" id={`${ids}-t`}>
          Novo serviço
        </p>
        <div className="form">
          <Row
            label="nome"
            htmlFor={`${ids}-n`}
            hint={taken ? <span className="field__problem">já existe um serviço com esse nome</span> : undefined}
          >
            <input
              id={`${ids}-n`}
              className={"input input--mono input--sm" + (taken ? " input--bad" : "")}
              required
              autoFocus
              value={name}
              placeholder="pedidos"
              aria-invalid={taken || undefined}
              onChange={(e) => setName(e.target.value)}
              spellCheck={false}
            />
          </Row>
          <h4 className="form__group">entrada</h4>
          <Row label="path" htmlFor={`${ids}-p`} hint="o path que o seu app chama no gateway; exato ou curinga de sufixo">
            <input
              id={`${ids}-p`}
              className="input input--mono input--sm"
              value={path}
              placeholder="/pedidos/*"
              onChange={(e) => setPath(e.target.value)}
              spellCheck={false}
            />
          </Row>
          {showHost ? (
            <Row label="host" htmlFor={`${ids}-h`} hint="vazio aceita qualquer Host">
              <input
                id={`${ids}-h`}
                className="input input--mono input--sm"
                autoFocus
                value={host}
                placeholder="pedidos.local"
                onChange={(e) => setHost(e.target.value)}
                spellCheck={false}
              />
            </Row>
          ) : (
            <Row label="">
              <button type="button" className="text-button" onClick={() => setShowHost(true)}>
                <PlusIcon /> host (opcional)
              </button>
            </Row>
          )}
          <h4 className="form__group">destino</h4>
          <Row
            label="url"
            htmlFor={`${ids}-u`}
            hint={
              destBad ? (
                <span className="field__problem">use uma URL http:// ou https://</span>
              ) : (
                "para onde o gateway redireciona; vazio, só as regras respondem"
              )
            }
          >
            <input
              id={`${ids}-u`}
              className={"input input--mono input--sm" + (destBad ? " input--bad" : "")}
              list={`${ids}-dests`}
              value={dest}
              placeholder={known[0] ?? "http://localhost:9004"}
              aria-invalid={destBad || undefined}
              onChange={(e) => setDest(e.target.value)}
              spellCheck={false}
            />
            <datalist id={`${ids}-dests`}>
              {known.map((u) => (
                <option key={u} value={u} />
              ))}
            </datalist>
          </Row>
          {known.length ? (
            <Row label="conhecidos">
              <span className="inline inline--wrap newsvc__known">
                {known.slice(0, QUICK_DESTINATIONS).map((u) => (
                  <button
                    key={u}
                    type="button"
                    className={"text-button mono" + (d === u ? " is-on" : "")}
                    aria-pressed={d === u}
                    title={`Usar ${u} como destino`}
                    onClick={() => setDest(u)}
                  >
                    {destinationText(u)}
                  </button>
                ))}
                {known.length > QUICK_DESTINATIONS ? (
                  <span className="dim">+{known.length - QUICK_DESTINATIONS} nas sugestões do campo</span>
                ) : null}
              </span>
            </Row>
          ) : null}
        </div>
        <p className="hint">
          Gravado em <span className="mono">routes/{n || "nome"}.yaml</span>. As regras de caos vêm depois, no próprio
          serviço.
        </p>
        {error ? <ErrorNote error={error} what="O serviço não foi criado" /> : null}
        <p className="inline">
          <button type="submit" className="button button--primary" disabled={sending || !n || taken || destBad}>
            {sending ? "Criando…" : "Criar serviço"}
          </button>
          <button type="button" className="button" onClick={onCancel}>
            Cancelar
          </button>
        </p>
      </form>
    </div>
  );
}
