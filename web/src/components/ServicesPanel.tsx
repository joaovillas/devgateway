import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import type { RouteResource, UpstreamHealth } from "../api";
import type { Load } from "../hooks";
import { useNow } from "../hooks";
import { percent } from "../format";
import type { Selection } from "../selection";
import {
  destinationText,
  filterRows,
  hasTicking,
  healthText,
  searchTerms,
  serviceRows,
  type ServiceRow,
} from "../services";
import { useStale } from "../live";
import { Segmented } from "./Controls";
import { PlusIcon } from "./Icons";
import { Panel } from "./Panel";
import { ServiceMap } from "./ServiceMap";
import { Empty, Failure, Loading } from "./States";

interface ServicesPanelProps {
  routes: Load<RouteResource[]>;
  upstreams: Load<UpstreamHealth[]>;
  /** Porta de tráfego, mostrada no nó "seu app" do mapa. */
  trafficPort: number | undefined;
  selection: Selection;
  onSelect: (s: Selection) => void;
  onRetry: () => void;
  /** O cadastro de serviço está aberto no painel ao lado. */
  creating: boolean;
  onCreate: () => void;
}

export type ServicesView = "map" | "list";

const VIEW_KEY = "gateway.painel.servicos";
const VIEW_OPTIONS: { value: ServicesView; label: string }[] = [
  { value: "map", label: "mapa" },
  { value: "list", label: "lista" },
];

/** A visão lembrada entre sessões; o mapa é o padrão. */
function readView(): ServicesView {
  try {
    return window.localStorage.getItem(VIEW_KEY) === "list" ? "list" : "map";
  } catch {
    return "map";
  }
}

/** Fora de campos de texto e de diálogos: onde os atalhos de uma tecla valem. */
function isTyping(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el) return false;
  return el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName) || el.closest("[role=alertdialog]") !== null;
}

/**
 * Os serviços: cada um é uma rota, com a entrada pela qual o app chama o
 * gateway, o destino para onde ele redireciona e a regra que está intervindo
 * agora. Duas visões do mesmo conjunto: o mapa (seu app → serviços →
 * destinos, o padrão) e a lista densa. Busca e cadastro ficam fixos no alto
 * e valem para as duas.
 */
export function ServicesPanel({
  routes,
  upstreams,
  trafficPort,
  selection,
  onSelect,
  onRetry,
  creating,
  onCreate,
}: ServicesPanelProps) {
  const [query, setQuery] = useState("");
  const [view, setViewRaw] = useState<ServicesView>(readView);
  const searchRef = useRef<HTMLInputElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const data = routes.kind === "ready" ? routes.data : null;
  // Instante em que o estado vivo chegou: base da conta local do TTL.
  const at = useMemo(() => Date.now(), [data]);
  const now = useNow(data ? hasTicking(data) : false);
  const stale = useStale();

  const setView = useCallback((v: ServicesView) => {
    setViewRaw(v);
    try {
      window.localStorage.setItem(VIEW_KEY, v);
    } catch {
      // Sem armazenamento, a escolha vale só para esta aba.
    }
  }, []);

  // "/" leva à busca e "n" abre o cadastro, de qualquer ponto fora de campos.
  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey || isTyping(e.target)) return;
      if (e.key === "/") {
        e.preventDefault();
        searchRef.current?.focus();
        searchRef.current?.select();
      } else if (e.key === "n") {
        e.preventDefault();
        onCreate();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onCreate]);

  const health = upstreams.kind === "ready" ? upstreams.data : [];
  const rows: ServiceRow[] = useMemo(() => (data ? serviceRows(data, health, now, at) : []), [data, health, now, at]);
  const terms = searchTerms(query);
  const shown = filterRows(rows, terms);

  let sub;
  let body;
  if (routes.kind === "loading") {
    body = <Loading what="os serviços" />;
  } else if (routes.kind === "error") {
    body = <Failure what="os serviços" request="GET /api/routes" error={routes.error} onRetry={onRetry} />;
  } else if (rows.length === 0) {
    body = (
      <Empty title="Nenhum serviço cadastrado">
        <p>
          Cadastre o primeiro em <strong>+ serviço</strong>: um nome, a entrada pela qual o seu app chama o gateway e o
          destino para onde ele redireciona.
        </p>
      </Empty>
    );
  } else {
    const ruled = rows.filter((r) => r.intervention).length;
    const down = new Set(rows.filter((r) => r.status === "down").map((r) => r.upstream)).size;
    sub = (
      <>
        <span className="mono">{terms.length ? `${shown.length} de ${rows.length}` : rows.length}</span>
        <span className="svc__subx">
          {ruled > 0 ? ` · ${ruled} com regra ativa` : null}
          {down > 0 ? ` · ${down} ${down === 1 ? "destino fora" : "destinos fora"}` : null}
        </span>
      </>
    );
    const notice =
      upstreams.kind === "error" ? (
        <p className="notice notice--state svc__notice">
          Estado dos destinos desconhecido: <span className="mono">GET /api/upstreams</span> respondeu{" "}
          <span className="mono">{upstreams.error.code}</span>.
        </p>
      ) : null;
    const none =
      shown.length === 0 ? (
        <Empty title={`Nenhum serviço com “${query.trim()}”`}>
          <p>
            A busca olha o nome, a entrada e o destino.{" "}
            <button type="button" className="link-button" onClick={() => setQuery("")}>
              Limpar a busca
            </button>
          </p>
        </Empty>
      ) : null;
    body = (
      <div className={view === "map" ? "svc svc--map" : "svc"} ref={bodyRef}>
        {notice}
        {view === "map" ? (
          (none ?? <ServiceMap rows={shown} trafficPort={trafficPort} selection={selection} stale={stale} onSelect={onSelect} />)
        ) : (
          <ServiceList rows={shown} none={none} selection={selection} stale={stale} onSelect={onSelect} />
        )}
      </div>
    );
  }

  const tools = (
    <>
      <Segmented<ServicesView> value={view} options={VIEW_OPTIONS} onChange={setView} label="Visão dos serviços" />
      <span className="search">
        <input
          ref={searchRef}
          className={"input input--sm search__input" + (query ? " input--on" : "")}
          type="search"
          value={query}
          placeholder="buscar"
          spellCheck={false}
          aria-label="Buscar serviços por nome, entrada ou destino"
          aria-keyshortcuts="/"
          title="Busca por nome, entrada ou destino (tecla /)"
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape" && query) {
              e.preventDefault();
              setQuery("");
            } else if (e.key === "Escape") {
              e.currentTarget.blur();
            } else if (e.key === "ArrowDown") {
              e.preventDefault();
              bodyRef.current?.querySelector<HTMLElement>("[data-col=main], [data-map-col=route]")?.focus();
            } else if (e.key === "Enter" && shown[0] && terms.length) {
              e.preventDefault();
              onSelect({ kind: "route", name: shown[0].name });
            }
          }}
        />
        {query ? null : <kbd className="search__kbd" aria-hidden="true">/</kbd>}
      </span>
      <button
        type="button"
        className={"button button--sm svc__new" + (creating ? " is-on" : "")}
        onClick={onCreate}
        aria-keyshortcuts="n"
        title="Cadastrar um serviço (tecla n)"
      >
        <PlusIcon /> serviço <kbd aria-hidden="true">n</kbd>
      </button>
    </>
  );

  return (
    <Panel id="services" title="serviços" sub={sub} tools={tools}>
      {body}
    </Panel>
  );
}

/** A visão em lista: uma linha por serviço, com colunas alinhadas. */
function ServiceList({
  rows: shown,
  none,
  selection,
  stale,
  onSelect,
}: {
  rows: ServiceRow[];
  none: ReactNode;
  selection: Selection;
  stale: boolean;
  onSelect: (s: Selection) => void;
}) {
  const listRef = useRef<HTMLUListElement>(null);

  // A linha selecionada (por clique, link ou cadastro) fica à vista na lista.
  const selKey = selection ? `${selection.kind}:${selection.name}` : "";
  useEffect(() => {
    if (selection?.kind !== "route") return;
    const li = [...(listRef.current?.children ?? [])].find((el) => (el as HTMLElement).dataset.name === selection.name);
    li?.scrollIntoView({ block: "nearest" });
    // A lista só precisa rolar quando a seleção muda ou quando a linha aparece.
  }, [selKey, shown.length]);

  return (
    <>
      <div className="svc__head" aria-hidden="true">
        <span className="svc__main">
          <span />
          <span>serviço</span>
          <span className="svc__entry">entrada</span>
        </span>
        <span>destino</span>
        <span className="svc__rulehead">regra</span>
      </div>
      {none ?? (
        <ul
          ref={listRef}
          className="svc__list"
          aria-label="Serviços. Setas para percorrer, Enter para selecionar, seta para a direita vai ao destino, Esc limpa a seleção."
          onKeyDown={onArrowKeys}
        >
          {shown.map((r, i) => (
            <Row
              key={r.name}
              row={r}
              tabbable={
                selection?.kind === "route"
                  ? r.name === selection.name || (i === 0 && !shown.some((x) => x.name === selection.name))
                  : i === 0
              }
              selection={selection}
              stale={stale}
              onSelect={onSelect}
            />
          ))}
        </ul>
      )}
    </>
  );
}

function Row({
  row: r,
  tabbable,
  selection,
  stale,
  onSelect,
}: {
  row: ServiceRow;
  tabbable: boolean;
  selection: Selection;
  stale: boolean;
  onSelect: (s: Selection) => void;
}) {
  const isSel = selection?.kind === "route" && selection.name === r.name;
  const destSel = selection?.kind === "upstream" && selection.name === r.upstream;
  // Com um destino selecionado, os serviços que não vão para ele recuam.
  const dim = selection?.kind === "upstream" && !destSel;
  const iv = r.intervention;
  const h = healthText(r.status, r.health);
  const down = r.status === "down";
  const label = [
    `Serviço ${r.name}`,
    r.entry ? `entrada ${r.entry}` : "entrada: qualquer requisição",
    r.upstream ? `destino ${r.upstream}, ${h.long}` : "sem destino",
    iv ? `regra ativa: ${iv.long}` : "sem regra ativa",
  ].join(", ");
  const cls = ["svc__row", isSel && "is-selected", dim && "is-dim", down && "is-down"].filter(Boolean).join(" ");

  return (
    <li className={cls} data-name={r.name} onClick={() => onSelect(isSel ? null : { kind: "route", name: r.name })}>
      <button
        type="button"
        className="svc__main"
        data-col="main"
        tabIndex={tabbable ? 0 : -1}
        aria-pressed={isSel}
        aria-label={label}
        title={r.upstream ? `${r.name} → ${r.upstream}\n${h.long}` : `${r.name}: sem destino`}
      >
        <span
          className={
            "dot " + (!r.upstream ? "dot--none" : down ? "dot--fault" : r.status === "up" ? "dot--healthy" : "dot--idle")
          }
        />
        <span className="svc__name">{r.name}</span>
        <span className={"svc__entry mono" + (r.entry ? "" : " dim")}>{r.entry || "qualquer"}</span>
      </button>
      {r.upstream ? (
        <button
          type="button"
          className={"svc__dest mono" + (destSel ? " is-on" : "")}
          data-col="dest"
          tabIndex={-1}
          aria-pressed={destSel}
          aria-label={`Filtrar o tráfego pelo destino ${r.upstream}: ${h.long}`}
          title={`${r.upstream}\n${h.long}\nClique para filtrar o tráfego por este destino`}
          onClick={(e) => {
            e.stopPropagation();
            onSelect(destSel ? null : { kind: "upstream", name: r.upstream! });
          }}
        >
          <span className="svc__arrow" aria-hidden="true">
            →
          </span>
          <span className="svc__host">{destinationText(r.upstream)}</span>
          {down ? <span className="svc__down">fora</span> : null}
        </button>
      ) : (
        <span className="svc__dest svc__dest--none">sem destino</span>
      )}
      <span className="svc__rule">
        {iv ? (
          <span
            className={`svc__tag svc__tag--${iv.kind}` + (stale ? " is-stale" : "")}
            title={stale ? `Sem conexão: estado da última atualização. ${iv.long}` : iv.long}
          >
            {iv.short} · {percent(iv.probability)}
            {iv.count > 1 ? <span className="dim"> +{iv.count - 1}</span> : null}
            <span
              className="svc__meter"
              aria-hidden="true"
              style={{ width: `${Math.min(1, Math.max(0, iv.probability)) * 100}%` }}
            />
          </span>
        ) : null}
      </span>
    </li>
  );
}

/**
 * Setas percorrem a lista: ↑ ↓ na mesma coluna (serviço ou destino), → do
 * serviço ao destino da linha, ← de volta. Home e End vão às pontas.
 */
function onArrowKeys(e: KeyboardEvent<HTMLUListElement>) {
  const target = e.target as HTMLElement;
  const col = target.dataset.col;
  if (!col) return;
  const inCol = (c: string) => [...e.currentTarget.querySelectorAll<HTMLElement>(`[data-col="${c}"]`)];
  const same = inCol(col);
  const i = same.indexOf(target);
  const row = target.closest("li");
  let next: HTMLElement | null | undefined;
  switch (e.key) {
    case "ArrowDown":
      next = same[Math.min(same.length - 1, i + 1)];
      break;
    case "ArrowUp":
      next = same[Math.max(0, i - 1)];
      break;
    case "Home":
      next = same[0];
      break;
    case "End":
      next = same.at(-1);
      break;
    case "ArrowRight":
      if (col === "main") next = row?.querySelector<HTMLElement>("[data-col=dest]");
      break;
    case "ArrowLeft":
      if (col === "dest") next = row?.querySelector<HTMLElement>("[data-col=main]");
      break;
    default:
      return;
  }
  e.preventDefault();
  next?.focus();
}
