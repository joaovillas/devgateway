import type { CSSProperties } from "react";
import type { RouteResource, UpstreamHealth } from "../api";
import type { Load } from "../hooks";
import { Panel } from "./Panel";
import { Empty, Failure, Loading } from "./States";

const ROW = 30;
const GAP = 6;

interface MapPanelProps {
  routes: Load<RouteResource[]>;
  upstreams: Load<UpstreamHealth[]>;
  selected: string | null;
  onSelect: (route: string | null) => void;
  onRetry: () => void;
}

export function MapPanel({ routes, upstreams, selected, onSelect, onRetry }: MapPanelProps) {
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
          Crie um documento em <span className="mono">routes/</span> e recarregue com{" "}
          <span className="mono">POST /api/reload</span>, ou crie a rota pela API com{" "}
          <span className="mono">POST /api/routes</span>.
        </p>
      </Empty>
    );
  } else {
    const health = upstreams.kind === "ready" ? upstreams.data : [];
    const graph = layout(routes.data);
    sub = `${routes.data.length} ${routes.data.length === 1 ? "rota" : "rotas"} · ${graph.upstreams.length} ${graph.upstreams.length === 1 ? "upstream" : "upstreams"}`;
    body = (
      <Graph
        graph={graph}
        health={health}
        healthError={upstreams.kind === "error"}
        selected={selected}
        onSelect={onSelect}
      />
    );
  }
  return (
    <Panel id="map" title="mapa" sub={sub}>
      {body}
    </Panel>
  );
}

interface GraphModel {
  routes: RouteResource[];
  upstreams: string[];
  height: number;
  clientY: number;
  routeY: number[];
  upstreamY: Map<string, number>;
}

function layout(routes: RouteResource[]): GraphModel {
  const upstreams: string[] = [];
  for (const r of routes) {
    const u = r.route.upstream;
    if (u && !upstreams.includes(u)) upstreams.push(u);
  }
  const rows = Math.max(routes.length, upstreams.length, 1);
  const height = rows * ROW + (rows - 1) * GAP;
  const center = (i: number) => i * (ROW + GAP) + ROW / 2;
  return {
    routes,
    upstreams,
    height,
    clientY: height / 2,
    routeY: routes.map((_, i) => center(i)),
    upstreamY: new Map(upstreams.map((u, i) => [u, center(i)])),
  };
}

function Graph({
  graph,
  health,
  healthError,
  selected,
  onSelect,
}: {
  graph: GraphModel;
  health: UpstreamHealth[];
  healthError: boolean;
  selected: string | null;
  onSelect: (route: string | null) => void;
}) {
  const { routes, upstreams, height, clientY, routeY, upstreamY } = graph;
  const style = { "--map-row": `${ROW}px`, "--map-gap": `${GAP}px` } as CSSProperties;
  const curve = (y1: number, y2: number) => `M0,${y1} C50,${y1} 50,${y2} 100,${y2}`;

  return (
    <div className="map" style={style} role="group" aria-label="Topologia cliente, rotas e upstreams">
      <span className="map__colhead">cliente</span>
      <span />
      <span className="map__colhead">rotas</span>
      <span />
      <span className="map__colhead">upstreams</span>

      <div style={{ paddingTop: clientY - ROW / 2 }}>
        <div className="node">
          <span className="node__name">cliente</span>
        </div>
      </div>

      <svg className="map__edges" viewBox={`0 0 100 ${height}`} preserveAspectRatio="none" height={height} aria-hidden="true">
        {routes.map((r, i) => (
          <path
            key={r.route.name}
            d={curve(clientY, routeY[i] ?? 0)}
            className={r.route.name === selected ? "is-selected" : undefined}
          />
        ))}
      </svg>

      <div className="map__stack">
        {routes.map((r) => {
          const active = Object.values(r.state ?? {}).filter((s) => s.active).length;
          const isSel = r.route.name === selected;
          const cls = ["node", isSel && "node--selected", active > 0 && "node--intervened"]
            .filter(Boolean)
            .join(" ");
          return (
            <button
              key={r.route.name}
              type="button"
              className={cls}
              aria-pressed={isSel}
              onClick={() => onSelect(isSel ? null : r.route.name)}
              title={matchText(r)}
            >
              <span className="node__name">{r.route.name}</span>
              <span className="node__meta mono">
                {active > 0 ? `${active} override${active > 1 ? "s" : ""} ativo${active > 1 ? "s" : ""}` : matchText(r)}
              </span>
            </button>
          );
        })}
      </div>

      <svg className="map__edges" viewBox={`0 0 100 ${height}`} preserveAspectRatio="none" height={height} aria-hidden="true">
        {routes.map((r, i) => {
          const u = r.route.upstream;
          const y2 = u ? upstreamY.get(u) : undefined;
          if (y2 === undefined) return null;
          return (
            <path
              key={r.route.name}
              d={curve(routeY[i] ?? 0, y2)}
              className={r.route.name === selected ? "is-selected" : undefined}
            />
          );
        })}
      </svg>

      <div className="map__stack">
        {upstreams.map((u) => {
          const h = health.find((x) => x.upstream === u);
          const status = h?.status ?? "unknown";
          const dot =
            status === "up" ? "dot dot--healthy" : status === "down" ? "dot dot--fault" : "dot dot--idle";
          const label =
            status === "up"
              ? "respondendo"
              : status === "down"
                ? `indisponível: ${h?.lastError ?? ""}`
                : healthError
                  ? "estado indisponível: GET /api/upstreams falhou"
                  : "sem tentativas recentes";
          return (
            <div key={u} className={"node" + (status === "down" ? " node--down" : "")} title={label}>
              <span className={dot} role="img" aria-label={label} />
              <span className="node__name mono">{u}</span>
            </div>
          );
        })}
        {upstreams.length === 0 ? (
          <div className="node node--ghost">
            <span className="node__name">nenhuma rota declara upstream</span>
          </div>
        ) : null}
      </div>
    </div>
  );
}

function matchText(r: RouteResource): string {
  const m = r.route.match;
  return [m.host, m.path].filter(Boolean).join(" ") || "—";
}
