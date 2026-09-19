import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import {
  api,
  type ConnectionState,
  type EventStream,
  type Exchange,
  type ExchangeFilter,
  type RouteResource,
  type Status,
} from "../api";
import { toApiError, useResource, type Load } from "../hooks";
import { clock, interventionTag, ms } from "../format";
import { isFiltering, matches, EMPTY_FILTER, type InterventionFilter, type ListFilter, type StatusClass } from "../exchangeFilter";
import type { Selection } from "../selection";
import { Segmented } from "./Controls";
import { ErrorNote } from "./ErrorNote";
import { HistoryDisabled, RecordingOff } from "./HistoryState";
import { CloseIcon } from "./Icons";
import { Panel } from "./Panel";
import { Empty, Failure, Loading } from "./States";
import type { ApiError } from "../api";

const LIMIT = 100;
const KEEP = 1000;
/** Menor fundo de escala do waterfall da lista: trocas de 2 ms não viram barras cheias. */
const MIN_SCALE_MS = 100;

interface TrafficPanelProps {
  stream: EventStream;
  connection: ConnectionState;
  status: Load<Status>;
  routes: Load<RouteResource[]>;
  /** Seleção do painel de serviços: serviço (rota) ou destino (upstream). */
  selection: Selection;
  /** Troca de rota pelo seletor da lista, sem mudar o que o painel de detalhe mostra. */
  onRoute: (name: string | null) => void;
  listFilter: ListFilter;
  onListFilter: (f: ListFilter) => void;
  /** Filtro completo (seleção + lista), no formato da API. */
  filter: ExchangeFilter;
  /** Troca aberta no detalhe, destacada e trazida à vista. */
  openId: string | null;
  onOpen: (ex: Exchange) => void;
}

export function TrafficPanel(props: TrafficPanelProps) {
  const { stream, connection, status, selection, filter, openId, onOpen } = props;
  // epoch muda quando a listagem precisa ser relida do zero: reconexão
  // (eventos perdidos não são reenviados), limpeza, troca de backend ou
  // mudança de history.* (exposição e registro podem ter sido religados).
  // A releitura por epoch mantém a lista à vista (reload); só um filtro novo
  // volta a "carregando", porque a lista anterior é de outra coisa.
  const [epoch, setEpoch] = useState(0);
  const filterKey = JSON.stringify(filter);
  const [page, reload] = useResource((signal) => api.listExchanges(filter, { limit: LIMIT }, signal), [filterKey]);
  const firstEpoch = useRef(epoch);
  useEffect(() => {
    if (epoch !== firstEpoch.current) reload();
  }, [epoch, reload]);
  const [live, setLive] = useState<Exchange[]>([]);
  const [more, setMore] = useState<{ items: Exchange[]; next: string; busy: boolean; error: ApiError | null }>({
    items: [],
    next: "",
    busy: false,
    error: null,
  });
  // Trocas que o servidor deixou de fora dos eventos (mais de 200 por segundo).
  const [missed, setMissed] = useState(0);

  // Página nova: as trocas ao vivo que ela já traz saem de `live`, mas as que
  // chegaram pelo SSE depois de o servidor montar a página (seq maior que a
  // maior da página) ficam, porque o servidor não as reenvia.
  const moreKey = useRef(0);
  useEffect(() => {
    moreKey.current++;
    if (page.kind === "ready") {
      const top = page.data.items.reduce((m, e) => Math.max(m, e.seq), -Infinity);
      setLive((prev) => prev.filter((e) => e.seq > top));
    } else {
      setLive([]);
    }
    setMissed(0);
    setMore({ items: [], next: page.kind === "ready" ? page.data.next : "", busy: false, error: null });
  }, [page]);

  const filterRef = useRef(filter);
  filterRef.current = filter;
  useEffect(() => {
    const bump = () => setEpoch((n) => n + 1);
    // Histórico limpo ou trocado: o que veio ao vivo antes não existe mais nele.
    const reset = () => {
      setLive([]);
      bump();
    };
    const offs = [
      stream.on("hello", bump),
      stream.on("history", reset),
      stream.on("config", (ev) => {
        if ((ev.settings ?? []).some((k) => k.startsWith("history."))) reset();
      }),
      stream.on("exchanges", (ev) => {
        if (ev.dropped > 0) setMissed((n) => n + ev.dropped);
        const fresh = ev.items.filter((x) => matches(x, filterRef.current));
        if (fresh.length === 0) return;
        // Os eventos chegam da mais antiga para a mais nova; a lista é o inverso.
        setLive((prev) => [...fresh.slice().reverse(), ...prev].slice(0, KEEP));
      }),
    ];
    return () => {
      for (const off of offs) off();
    };
  }, [stream]);

  const loadMore = () => {
    if (!more.next || more.busy) return;
    setMore((m) => ({ ...m, busy: true, error: null }));
    // A resposta só vale para a lista que a pediu: filtro ou página trocados
    // no meio do caminho a descartam.
    const asked = moreKey.current;
    api.listExchanges(filter, { limit: LIMIT, cursor: more.next }).then(
      (p) => {
        if (moreKey.current !== asked) return;
        setMore((m) => ({ items: [...m.items, ...p.items], next: p.next, busy: false, error: null }));
      },
      (e: unknown) => {
        if (moreKey.current !== asked) return;
        setMore((m) => ({ ...m, busy: false, error: toApiError(e) }));
      },
    );
  };

  const liveIds = useMemo(() => new Set(live.map((e) => e.id)), [live]);
  const rows = useMemo(() => {
    if (page.kind !== "ready") return [];
    const seen = new Set<string>();
    const out: Exchange[] = [];
    for (const e of [...live, ...page.data.items, ...more.items]) {
      if (seen.has(e.id)) continue;
      seen.add(e.id);
      out.push(e);
    }
    return out.slice(0, KEEP + more.items.length);
  }, [page, live, more.items]);

  const filtered = isFiltering(props.listFilter) || selection !== null;
  const legend = (
    <span className="legend" aria-hidden="true">
      <span>
        <i style={{ background: "var(--time-upstream)" }} /> destino
      </span>
      <span>
        <i style={{ background: "var(--injected)" }} /> injetado
      </span>
      <span>
        <i style={{ background: "var(--time-gateway)" }} /> gateway
      </span>
    </span>
  );

  // O que o painel está recebendo agora, sem esconder a pausa.
  const sub =
    page.kind === "error" && page.error.code === "history_disabled" ? (
      <span>desabilitado</span>
    ) : connection.kind === "open" ? (
      <span className="live">
        <span className="dot dot--healthy" aria-hidden="true" /> ao vivo
      </span>
    ) : (
      <span className="live live--paused" title="Sem conexão com /api/events: a lista não recebe trocas novas até a reconexão">
        <span className="dot dot--fault" aria-hidden="true" /> parado, sem conexão
      </span>
    );

  let body;
  if (page.kind === "loading") {
    body = <Loading what="o histórico" />;
  } else if (page.kind === "error") {
    const e = page.error;
    body =
      e.code === "history_disabled" ? (
        <HistoryDisabled error={e} />
      ) : (
        <Failure what="o histórico" request="GET /api/exchanges" error={e} onRetry={reload} />
      );
  } else if (rows.length === 0) {
    body = !page.data.recording ? (
      <RecordingOff />
    ) : filtered ? (
      <Empty title="Nenhuma troca com esse filtro">
        <p>
          O histórico está gravando, mas nenhuma troca registrada combina com o filtro. As que combinarem aparecem aqui
          sozinhas.{" "}
          <button
            type="button"
            className="link-button"
            onClick={() => {
              props.onListFilter(EMPTY_FILTER);
              props.onRoute(null);
            }}
          >
            Limpar os filtros
          </button>
        </p>
      </Empty>
    ) : (
      <Empty title="Nenhuma troca ainda">
        <p>
          O histórico está ligado e gravando em <span className="mono">{page.data.backend}</span>. As trocas aparecem
          aqui sozinhas assim que uma requisição atravessar a porta de tráfego
          {status.kind === "ready" ? (
            <>
              {" "}
              <span className="mono">:{status.data.ports.traffic}</span>
            </>
          ) : null}
          .
        </p>
      </Empty>
    );
  } else {
    body = (
      <>
        {!page.data.recording ? <RecordingOff compact /> : null}
        {missed > 0 ? (
          <p className="notice notice--state" role="status">
            {missed} {missed === 1 ? "troca chegou" : "trocas chegaram"} rápido demais para o fluxo ao vivo e não{" "}
            {missed === 1 ? "aparece" : "aparecem"} na lista.{" "}
            <button type="button" className="link-button" onClick={reload}>
              Reler o histórico
            </button>
          </p>
        ) : null}
        <TrafficTable
          rows={rows}
          fresh={liveIds}
          openId={openId}
          onOpen={onOpen}
          showRoute={selection?.kind !== "route"}
        />
        {more.next ? (
          <div className="traffic__more">
            <button type="button" className="text-button" onClick={loadMore} disabled={more.busy}>
              {more.busy ? "lendo…" : `carregar ${LIMIT} trocas mais antigas`}
            </button>
            {more.error ? <ErrorNote error={more.error} what="A página seguinte não veio" /> : null}
          </div>
        ) : more.items.length ? (
          <p className="traffic__end dim">fim do histórico com este filtro</p>
        ) : null}
      </>
    );
  }

  return (
    <Panel id="traffic" title="tráfego" sub={sub} tools={legend}>
      <div className="traffic-panel">
        <FilterBar {...props} />
        <div className="traffic-scroll">{body}</div>
      </div>
    </Panel>
  );
}

const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"];

/** Filtros do contrato: serviço (ou o destino do painel de serviços), método, status, intervenção e path. */
function FilterBar({ routes, selection, onRoute, listFilter: f, onListFilter }: TrafficPanelProps) {
  const [path, setPath] = useState(f.path);
  // Filtro limpo de fora (botão "limpar"): o campo acompanha.
  useEffect(() => setPath(f.path), [f.path]);
  useEffect(() => {
    if (path === f.path) return;
    const t = window.setTimeout(() => onListFilter({ ...f, path }), 250);
    return () => window.clearTimeout(t);
  }, [path, f, onListFilter]);

  const names = routes.kind === "ready" ? routes.data.map((r) => r.route.name) : [];
  const routeName = selection?.kind === "route" ? selection.name : "";
  const any = isFiltering(f) || selection !== null;

  return (
    <div className="filters" role="search" aria-label="Filtrar o tráfego">
      {selection?.kind === "upstream" ? (
        <span className="chip chip--override">
          destino {selection.name.replace(/^https?:\/\//, "")}
          <button type="button" onClick={() => onRoute(null)} aria-label={`Remover o filtro do destino ${selection.name}`}>
            <CloseIcon />
          </button>
        </span>
      ) : (
        <label className="filters__item">
          <span className="filters__label">serviço</span>
          <select
            className={"input input--sm select" + (routeName ? " select--on" : "")}
            value={routeName}
            onChange={(e) => onRoute(e.target.value || null)}
          >
            <option value="">todas</option>
            {routeName && !names.includes(routeName) ? <option value={routeName}>{routeName}</option> : null}
            {names.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
        </label>
      )}
      <label className="filters__item">
        <span className="filters__label">método</span>
        <select
          className={"input input--sm select" + (f.method ? " select--on" : "")}
          value={f.method}
          onChange={(e) => onListFilter({ ...f, method: e.target.value })}
        >
          <option value="">todos</option>
          {METHODS.map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
        </select>
      </label>
      <span className="filters__item">
        <span className="filters__label" aria-hidden="true">
          status
        </span>
        <Segmented<StatusClass>
          label="Status"
          value={f.status}
          onChange={(status) => onListFilter({ ...f, status })}
          options={[
            { value: "all", label: "todos" },
            { value: "2xx", label: "2xx" },
            { value: "3xx", label: "3xx" },
            { value: "4xx", label: "4xx" },
            { value: "5xx", label: "5xx" },
          ]}
        />
      </span>
      <span className="filters__item">
        <span className="filters__label" aria-hidden="true">
          intervenção
        </span>
        <Segmented<InterventionFilter>
          label="Intervenção"
          value={f.intervened}
          onChange={(intervened) => onListFilter({ ...f, intervened })}
          options={[
            { value: "all", label: "todas" },
            { value: "yes", label: "com" },
            { value: "no", label: "sem" },
          ]}
        />
      </span>
      <label className="filters__item filters__item--grow">
        <span className="filters__label">path</span>
        <input
          className={"input input--sm input--mono" + (f.path ? " input--on" : "")}
          type="search"
          value={path}
          placeholder="contém…"
          spellCheck={false}
          onChange={(e) => setPath(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") onListFilter({ ...f, path });
          }}
        />
      </label>
      {any ? (
        <button
          type="button"
          className="text-button"
          onClick={() => {
            onListFilter(EMPTY_FILTER);
            onRoute(null);
          }}
        >
          limpar
        </button>
      ) : null}
    </div>
  );
}

function TrafficTable({
  rows,
  fresh,
  openId,
  onOpen,
  showRoute,
}: {
  rows: Exchange[];
  fresh: Set<string>;
  openId: string | null;
  onOpen: (ex: Exchange) => void;
  /** Com o filtro de uma rota, a coluna repetiria o mesmo nome em toda linha e sai; o path fica com a largura. */
  showRoute: boolean;
}) {
  const body = useRef<HTMLTableSectionElement>(null);
  const openRow = useRef<HTMLTableRowElement>(null);
  const lastOpen = useRef<string | null>(null);
  useEffect(() => {
    // Rola só a lista (não a página) até a troca aberta, descontando o cabeçalho fixo.
    const row = openRow.current;
    const box = row?.closest(".traffic-scroll");
    if (row && box) {
      const head = (box.querySelector("thead") as HTMLElement | null)?.offsetHeight ?? 0;
      const r = row.getBoundingClientRect();
      const b = box.getBoundingClientRect();
      if (r.top < b.top + head) box.scrollTop -= b.top + head - r.top;
      else if (r.bottom > b.bottom) box.scrollTop += r.bottom - b.bottom;
    }
    // Detalhe fechado (Esc ou botão): o foco, que estava nele e sumiu, volta à linha da troca.
    const closed = lastOpen.current;
    lastOpen.current = openId;
    if (!openId && closed && (document.activeElement === document.body || document.activeElement === null)) {
      const i = rows.findIndex((r) => r.id === closed);
      (body.current?.children[i] as HTMLElement | undefined)?.focus();
    }
  }, [openId]);
  // Escala linear comum a todas as linhas visíveis, com um fundo mínimo.
  const max = Math.max(MIN_SCALE_MS, ...rows.map((r) => r.timing.totalMs));
  const focusIndex = Math.max(0, rows.findIndex((r) => r.id === openId));

  const onKey = (e: KeyboardEvent<HTMLTableRowElement>, i: number) => {
    let next = -1;
    if (e.key === "ArrowDown") next = Math.min(rows.length - 1, i + 1);
    else if (e.key === "ArrowUp") next = Math.max(0, i - 1);
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = rows.length - 1;
    else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onOpen(rows[i]!);
      return;
    }
    if (next < 0) return;
    e.preventDefault();
    const el = body.current?.children[next] as HTMLElement | undefined;
    el?.focus();
    // Com uma troca aberta, andar pela lista abre a troca de cada linha.
    if (openId) onOpen(rows[next]!);
  };

  return (
    <table className="traffic">
      <caption className="sr-only">
        Trocas capturadas, da mais nova para a mais antiga. Setas percorrem as linhas e Enter abre o detalhe.
      </caption>
      <colgroup>
        <col className="traffic__c-time" />
        <col className="traffic__c-method" />
        <col />
        {showRoute ? <col className="traffic__c-route" /> : null}
        <col className="traffic__c-status" />
        <col className="traffic__c-tag traffic__tag" />
        <col className="traffic__c-total" />
        <col className="traffic__c-fall" />
      </colgroup>
      <thead>
        <tr>
          <th scope="col" className="traffic__time">
            hora
          </th>
          <th scope="col">método</th>
          <th scope="col">path</th>
          {showRoute ? (
            <th scope="col" className="traffic__route">
              serviço
            </th>
          ) : null}
          <th scope="col" className="num">
            status
          </th>
          <th scope="col" className="traffic__tag">
            intervenção
          </th>
          <th scope="col" className="num">
            total
          </th>
          <th scope="col" className="traffic__fallhead">
            <span className="traffic__fallword">tempo</span>{" "}
            <span className="traffic__scale mono">0–{ms(max)}</span>
          </th>
        </tr>
      </thead>
      <tbody ref={body}>
        {rows.map((e, i) => {
          const tag = interventionTag(e);
          const status = e.status ? String(e.status) : "—";
          // Vermelho só para o que o gateway fez (queda ou sintetizado); um 500
          // do upstream não é intervenção e fica na cor do texto, sublinhado.
          const fault = e.outcome === "dropped" || e.outcome === "synthesized";
          // O erro do próprio gateway não é do upstream: fica no cinza da etiqueta.
          const gatewayError = e.outcome === "gateway";
          const upstreamError = !fault && !gatewayError && (e.status ?? 0) >= 500;
          const open = e.id === openId;
          return (
            <tr
              key={e.id}
              ref={open ? openRow : undefined}
              tabIndex={i === focusIndex ? 0 : -1}
              aria-current={open ? "true" : undefined}
              onClick={() => onOpen(e)}
              onKeyDown={(ev) => onKey(ev, i)}
              className={
                [fresh.has(e.id) ? "is-new" : "", open ? "is-open" : "", tag ? "is-intervened" : ""].join(" ").trim() ||
                undefined
              }
            >
              <td className="mono dim traffic__time">{clock(e.start)}</td>
              <td className="mono">{e.method}</td>
              <td className="mono" title={e.path + (e.query ? "?" + e.query : "")}>
                {e.path}
                {e.query ? <span className="dim">?{e.query}</span> : null}
              </td>
              {showRoute ? (
                <td className={"traffic__route" + (e.route ? "" : " dim")} title={e.route ?? undefined}>
                  {e.route ?? "sem serviço"}
                </td>
              ) : null}
              <td
                className={
                  "mono num" +
                  (fault
                    ? " status--fault"
                    : gatewayError
                      ? " status--gateway"
                      : upstreamError
                        ? " status--upstream-error"
                        : "")
                }
                title={gatewayError ? "erro do próprio gateway" : upstreamError ? "erro do destino" : undefined}
              >
                {/* Estreito, a coluna de intervenção sai: o status que o gateway
                    sintetizou ou derrubou vira uma etiqueta em caixa, e o nome da
                    intervenção segue para o leitor de tela. */}
                <span className={tag && tag.tone !== "delay" ? "status-mark" : undefined}>{status}</span>
                {tag ? <span className="traffic__narrow-tag"> {tag.label}</span> : null}
              </td>
              <td className="traffic__tag">
                {tag ? (
                  <span className={`tag tag--${tag.tone}`}>{tag.label}</span>
                ) : gatewayError ? (
                  <span className="tag tag--gateway">
                    <span className="tag__long">erro do </span>gateway
                  </span>
                ) : null}
              </td>
              <td className="mono num">{ms(e.timing.totalMs)}</td>
              <td>
                <Waterfall ex={e} max={max} />
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

/** Um segmento por grandeza, na ordem do caminho: upstream, atraso injetado, overhead. */
function Waterfall({ ex, max }: { ex: Exchange; max: number }) {
  const t = ex.timing;
  const pct = (v: number) => `${(v / max) * 100}%`;
  const label = `destino ${ms(t.upstreamMs)}, injetado ${ms(t.injectedMs)}, gateway ${ms(t.gatewayMs)}`;
  return (
    <div className="fall" role="img" aria-label={label} title={label}>
      {t.upstreamMs > 0 ? <span className="fall__seg fall__seg--upstream" style={{ width: pct(t.upstreamMs) }} /> : null}
      {t.injectedMs > 0 ? <span className="fall__seg fall__seg--injected" style={{ width: pct(t.injectedMs) }} /> : null}
      {t.gatewayMs > 0 ? <span className="fall__seg fall__seg--gateway" style={{ width: pct(t.gatewayMs) }} /> : null}
    </div>
  );
}
