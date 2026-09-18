import { useMemo, type CSSProperties, type KeyboardEvent } from "react";
import type { RouteResource, UpstreamHealth } from "../api";
import type { Load } from "../hooks";
import { useNow } from "../hooks";
import { percent } from "../format";
import type { Selection } from "../selection";
import { hasTicking, routeIntervention, type RouteIntervention } from "../topology";
import { useStale } from "../live";
import { Panel } from "./Panel";
import { Empty, Failure, Loading } from "./States";

const ROW = 34;
const GAP = 8;
const PITCH = ROW + GAP;

interface MapPanelProps {
  routes: Load<RouteResource[]>;
  upstreams: Load<UpstreamHealth[]>;
  trafficPort: number | undefined;
  selection: Selection;
  onSelect: (s: Selection) => void;
  onRetry: () => void;
}

export function MapPanel({ routes, upstreams, trafficPort, selection, onSelect, onRetry }: MapPanelProps) {
  const data = routes.kind === "ready" ? routes.data : null;
  // Instante em que o estado vivo chegou: base da conta local do TTL.
  const at = useMemo(() => Date.now(), [data]);
  const now = useNow(data ? hasTicking(data) : false);

  let sub: string | undefined;
  let body;
  if (routes.kind === "loading") {
    body = <Loading what="as rotas" />;
  } else if (routes.kind === "error") {
    body = <Failure what="as rotas" request="GET /api/routes" error={routes.error} onRetry={onRetry} />;
  } else if (routes.data.length === 0) {
    body = (
      <Empty title="Nenhuma rota configurada">
        <p>
          Crie a primeira em <strong>nova rota</strong>, no painel ao lado.
        </p>
      </Empty>
    );
  } else {
    const health = upstreams.kind === "ready" ? upstreams.data : [];
    const graph = layout(routes.data, health, now, at);
    const intervened = graph.routes.filter((r) => r.intervention).length;
    const down = graph.upstreams.filter((u) => u.status === "down").length;
    sub = [
      count(graph.routes.length, "rota", "rotas"),
      count(graph.upstreams.length, "upstream", "upstreams"),
      intervened > 0 ? `${intervened} com override ativo` : null,
      down > 0 ? count(down, "indisponível", "indisponíveis") : null,
    ]
      .filter(Boolean)
      .join(" · ");
    body = (
      <>
        {upstreams.kind === "error" ? (
          <p className="notice">
            Disponibilidade dos upstreams desconhecida: <span className="mono">GET /api/upstreams</span> respondeu{" "}
            <span className="mono">{upstreams.error.code}</span>.
          </p>
        ) : null}
        <Graph graph={graph} trafficPort={trafficPort} selection={selection} onSelect={onSelect} />
      </>
    );
  }
  return (
    <Panel id="map" title="mapa" sub={sub}>
      {body}
    </Panel>
  );
}

function count(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`;
}

// ---------- Disposição ----------

interface RouteNode {
  res: RouteResource;
  name: string;
  upstream: string | undefined;
  y: number;
  intervention: RouteIntervention | null;
}

interface UpstreamNode {
  url: string;
  y: number;
  status: UpstreamHealth["status"];
  health: UpstreamHealth | undefined;
}

interface GraphModel {
  routes: RouteNode[];
  upstreams: UpstreamNode[];
  height: number;
  clientY: number;
}

/**
 * Rotas em fila, na ordem de precedência. Cada upstream fica na altura média
 * das rotas que apontam para ele, empurrado para baixo quando colidiria com o
 * anterior: menos cruzamentos sem biblioteca de grafo.
 */
function layout(routes: RouteResource[], health: UpstreamHealth[], now: number, at: number): GraphModel {
  const rnodes: RouteNode[] = routes.map((res, i) => ({
    res,
    name: res.route.name,
    upstream: res.route.upstream || undefined,
    y: i * PITCH + ROW / 2,
    intervention: routeIntervention(res, now, at),
  }));

  const ys = new Map<string, number[]>();
  for (const r of rnodes) {
    if (!r.upstream) continue;
    const list = ys.get(r.upstream) ?? [];
    list.push(r.y);
    ys.set(r.upstream, list);
  }
  const wanted = [...ys.entries()]
    .map(([url, list], order) => ({ url, order, mean: list.reduce((a, b) => a + b, 0) / list.length }))
    .sort((a, b) => a.mean - b.mean || a.order - b.order);

  const unodes: UpstreamNode[] = [];
  let prev = -Infinity;
  for (const w of wanted) {
    const y = Math.max(w.mean, prev + PITCH, ROW / 2);
    prev = y;
    const h = health.find((x) => x.upstream === w.url);
    unodes.push({ url: w.url, y, status: h?.status ?? "unknown", health: h });
  }

  const bottom = Math.max(rnodes.at(-1)?.y ?? 0, unodes.at(-1)?.y ?? 0) + ROW / 2;
  return { routes: rnodes, upstreams: unodes, height: bottom, clientY: Math.min(bottom / 2, ROW / 2 + PITCH * 2) };
}

// ---------- Desenho ----------

function Graph({
  graph,
  trafficPort,
  selection,
  onSelect,
}: {
  graph: GraphModel;
  trafficPort: number | undefined;
  selection: Selection;
  onSelect: (s: Selection) => void;
}) {
  const { routes, upstreams, height, clientY } = graph;
  const style = { "--map-row": `${ROW}px`, "--map-h": `${height}px` } as CSSProperties;
  const curve = (y1: number, y2: number) => `M0,${y1} C50,${y1} 50,${y2} 100,${y2}`;

  // Com seleção, o que não pertence ao caminho selecionado recua.
  const liveRoutes = new Set<string>();
  const liveUpstreams = new Set<string>();
  if (selection?.kind === "route") {
    liveRoutes.add(selection.name);
    const u = routes.find((r) => r.name === selection.name)?.upstream;
    if (u) liveUpstreams.add(u);
  } else if (selection?.kind === "upstream") {
    liveUpstreams.add(selection.name);
    for (const r of routes) if (r.upstream === selection.name) liveRoutes.add(r.name);
  }
  const dimRoute = (name: string) => selection !== null && !liveRoutes.has(name);
  const dimUpstream = (url: string) => selection !== null && !liveUpstreams.has(url);
  const statusOf = new Map(upstreams.map((u) => [u.url, u.status]));
  const shortNames = shortUpstreamNames(upstreams.map((u) => u.url));
  const stale = useStale();

  return (
    <div
      className={"map" + (selection ? " map--raised" : "")}
      style={style}
      role="group"
      aria-label="Topologia: cliente, rotas e upstreams. Setas para percorrer, Enter para selecionar, Esc para limpar."
      onKeyDown={onArrowKeys}
    >
      <span className="map__colhead">cliente</span>
      <span />
      <span className="map__colhead">rotas</span>
      <span />
      <span className="map__colhead">upstreams</span>

      <div className="map__col">
        <div className="node node--client" style={{ top: clientY - ROW / 2 }}>
          <span className="node__name">cliente</span>
          {trafficPort ? <span className="node__meta mono">:{trafficPort}</span> : null}
        </div>
      </div>

      <svg className="map__edges" viewBox={`0 0 100 ${height}`} preserveAspectRatio="none" aria-hidden="true">
        {routes.map((r) => (
          <path
            key={r.name}
            d={curve(clientY, r.y)}
            className={edgeClass(dimRoute(r.name), selection !== null && !dimRoute(r.name), false)}
          />
        ))}
      </svg>

      <div className="map__col">
        {routes.map((r) => {
          const isSel = selection?.kind === "route" && selection.name === r.name;
          const iv = r.intervention;
          const cls = [
            "node",
            "node--route",
            isSel && "node--selected",
            iv && "node--intervened",
            dimRoute(r.name) && "is-dim",
          ]
            .filter(Boolean)
            .join(" ");
          const where = matchText(r.res);
          const label = [
            `Rota ${r.name}`,
            where !== "—" ? `casa ${where}` : null,
            r.upstream ? `encaminha para ${r.upstream}` : "sem upstream",
            iv ? `override ativo: ${iv.long}` : "sem override ativo",
          ]
            .filter(Boolean)
            .join(", ");
          return (
            <button
              key={r.name}
              type="button"
              className={cls}
              style={{ top: r.y - ROW / 2 }}
              data-map-col="route"
              data-map-key={r.upstream ?? ""}
              aria-pressed={isSel}
              aria-label={label}
              title={iv ? `${where} · ${iv.long}` : where}
              onClick={() => onSelect(isSel ? null : { kind: "route", name: r.name })}
            >
              <span className="node__name">{r.name}</span>
              <span className="node__meta mono">{r.upstream ? where : "sem upstream"}</span>
              {iv ? (
                <>
                  <span
                    className={`node__tag node__tag--${iv.kind}` + (stale ? " is-stale" : "")}
                    title={stale ? "Sem conexão: estado da última atualização" : undefined}
                  >
                    {iv.short} · {percent(iv.probability)}
                    {iv.count > 1 ? <span className="dim"> +{iv.count - 1}</span> : null}
                    {stale ? <span className="node__stale"> · parado</span> : null}
                  </span>
                  <span
                    className={`node__meter node__meter--${iv.kind}`}
                    style={{ width: `${Math.min(1, Math.max(0, iv.probability)) * 100}%` }}
                  />
                </>
              ) : null}
            </button>
          );
        })}
      </div>

      <svg className="map__edges" viewBox={`0 0 100 ${height}`} preserveAspectRatio="none" aria-hidden="true">
        {routes.map((r) => {
          if (!r.upstream) return null;
          const u = upstreams.find((x) => x.url === r.upstream);
          if (!u) return null;
          const dim = dimRoute(r.name) || dimUpstream(u.url);
          return (
            <path
              key={r.name}
              d={curve(r.y, u.y)}
              className={edgeClass(dim, selection !== null && !dim, statusOf.get(u.url) === "down")}
            />
          );
        })}
      </svg>

      <div className="map__col">
        {upstreams.map((u) => {
          const isSel = selection?.kind === "upstream" && selection.name === u.url;
          const text = upstreamText(u);
          const cls = [
            "node",
            "node--upstream",
            isSel && "node--selected",
            u.status === "down" && "node--down",
            dimUpstream(u.url) && "is-dim",
          ]
            .filter(Boolean)
            .join(" ");
          return (
            <button
              key={u.url}
              type="button"
              className={cls}
              style={{ top: u.y - ROW / 2 }}
              data-map-col="upstream"
              data-map-key={u.url}
              aria-pressed={isSel}
              aria-label={`Upstream ${u.url}: ${text.long}`}
              title={`${u.url}\n${text.long}`}
              onClick={() => onSelect(isSel ? null : { kind: "upstream", name: u.url })}
            >
              <span className={`dot dot--${u.status === "up" ? "healthy" : u.status === "down" ? "fault" : "idle"}`} />
              <span className="node__name mono">
                <span className="node__long">{u.url.replace(/^https?:\/\//, "")}</span>
                <span className="node__short" aria-hidden="true">
                  {shortNames.get(u.url)}
                </span>
              </span>
              <span className={"node__meta mono" + (u.status === "down" ? " is-fault" : "")}>
                <span className="node__long">{text.short}</span>
                {u.status === "down" ? (
                  <span className="node__short" aria-hidden="true">
                    fora
                  </span>
                ) : null}
              </span>
            </button>
          );
        })}
        {upstreams.length === 0 ? (
          <div className="node node--ghost" style={{ top: 0 }}>
            <span className="node__name">nenhuma rota declara upstream</span>
          </div>
        ) : null}
      </div>
    </div>
  );
}

/**
 * Nome curto de cada upstream para o mapa estreito: a porta (":9001"), que é
 * o que costuma diferenciá-los no desenvolvimento local, ou host:porta quando
 * só a porta repetiria.
 */
function shortUpstreamNames(urls: string[]): Map<string, string> {
  const parts = urls.map((url) => {
    try {
      const u = new URL(url);
      return { url, host: u.hostname, port: u.port };
    } catch {
      return { url, host: url.replace(/^https?:\/\//, ""), port: "" };
    }
  });
  const byPort = new Map<string, number>();
  for (const p of parts) if (p.port) byPort.set(p.port, (byPort.get(p.port) ?? 0) + 1);
  return new Map(
    parts.map((p) => [
      p.url,
      p.port ? (byPort.get(p.port)! > 1 ? `${p.host}:${p.port}` : `:${p.port}`) : p.host,
    ]),
  );
}

function edgeClass(dim: boolean, lit: boolean, down: boolean): string | undefined {
  return [dim && "is-dim", lit && "is-selected", down && "is-down"].filter(Boolean).join(" ") || undefined;
}

function upstreamText(u: UpstreamNode): { short: string; long: string } {
  const h = u.health;
  if (u.status === "down") {
    return {
      short: "indisponível",
      long: `indisponível, ${h?.recent.failures ?? 0} de ${h?.recent.attempts ?? 0} tentativas recentes falharam${h?.lastError ? `: ${h.lastError}` : ""}`,
    };
  }
  if (u.status === "up") {
    const f = h?.recent.failures ?? 0;
    return {
      short: f > 0 ? `${f}/${h?.recent.attempts} falhas` : "respondendo",
      long: f > 0 ? `respondendo, ${f} de ${h?.recent.attempts} tentativas recentes falharam` : "respondendo",
    };
  }
  return { short: "sem tentativas", long: "nenhuma tentativa de encaminhamento recente" };
}

function matchText(r: RouteResource): string {
  const m = r.route.match;
  return [m.host, m.path].filter(Boolean).join(" ") || "—";
}

/**
 * Setas percorrem o mapa: ↑ ↓ dentro da coluna, ← → entre rotas e upstreams
 * (indo ao upstream da rota ou à primeira rota do upstream).
 */
function onArrowKeys(e: KeyboardEvent<HTMLDivElement>) {
  const target = e.target as HTMLElement;
  const col = target.dataset.mapCol;
  if (!col) return;
  const all = (c: string) => [...e.currentTarget.querySelectorAll<HTMLElement>(`[data-map-col="${c}"]`)];
  const same = all(col);
  const i = same.indexOf(target);
  let next: HTMLElement | undefined;
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
      if (col === "route") {
        const key = target.dataset.mapKey;
        next = all("upstream").find((u) => u.dataset.mapKey === key) ?? all("upstream")[0];
      }
      break;
    case "ArrowLeft":
      if (col === "upstream") {
        const key = target.dataset.mapKey;
        next = all("route").find((r) => r.dataset.mapKey === key) ?? all("route")[0];
      }
      break;
    default:
      return;
  }
  e.preventDefault();
  next?.focus();
}
