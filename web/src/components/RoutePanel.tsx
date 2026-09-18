import { useMemo } from "react";
import type { RouteResource } from "../api";
import type { Load } from "../hooks";
import { useNow } from "../hooks";
import { liveSummary, overrideSelector, overrideSummary } from "../format";
import { Panel } from "./Panel";
import { Empty, Loading } from "./States";

interface RoutePanelProps {
  routes: Load<RouteResource[]>;
  selected: string | null;
  onRetry: () => void;
}

/** Controles da rota selecionada no mapa. */
export function RoutePanel({ routes, selected, onRetry }: RoutePanelProps) {
  let body;
  let sub: string | undefined;
  if (routes.kind === "loading") {
    body = <Loading what="as rotas" />;
  } else if (routes.kind === "error") {
    // O mapa já mostra a falha completa; aqui só o necessário para não repetir.
    body = (
      <Empty title="Rotas indisponíveis">
        <p>
          <span className="mono">GET /api/routes</span> falhou ({routes.error.code}). O detalhe está no mapa.{" "}
          <button type="button" className="link-button" onClick={onRetry}>
            Tentar de novo
          </button>
        </p>
      </Empty>
    );
  } else if (!selected) {
    body = (
      <Empty title="Nenhuma rota selecionada">
        <p>Selecione uma rota no mapa para ver o upstream, o casamento e os overrides dela.</p>
      </Empty>
    );
  } else {
    const r = routes.data.find((x) => x.route.name === selected);
    if (!r) {
      body = (
        <Empty title={`A rota ${selected} não existe mais`}>
          <p>Ela foi removida ou renomeada desde que foi selecionada.</p>
        </Empty>
      );
    } else {
      sub = r.route.name;
      body = <RouteDetail res={r} />;
    }
  }
  return (
    <Panel id="route" title="rota" sub={sub}>
      {body}
    </Panel>
  );
}

function RouteDetail({ res }: { res: RouteResource }) {
  const r = res.route;
  const overrides = r.overrides ?? [];
  // Instante em que este estado vivo chegou: base para a contagem regressiva do TTL.
  const at = useMemo(() => Date.now(), [res]);
  const ticking = overrides.some((o) => res.state[o.name]?.ttlRemainingMs != null);
  const now = useNow(ticking);

  return (
    <div className="route">
      <dl className="fields">
        <dt>upstream</dt>
        <dd className="mono">{r.upstream || <span className="dim">nenhum: só overrides respondem</span>}</dd>
        <dt>host</dt>
        <dd className="mono">{r.match.host || <span className="dim">qualquer</span>}</dd>
        <dt>path</dt>
        <dd className="mono">{r.match.path || <span className="dim">qualquer</span>}</dd>
        <dt>remove prefixo</dt>
        <dd>{r.stripPrefix ? "sim" : "não"}</dd>
        <dt>reescreve host</dt>
        <dd>{r.rewriteHost ? "sim" : "não, repassa o Host original"}</dd>
        <dt>timeout</dt>
        <dd className="mono">{r.timeout ?? <span className="dim">sem limite próprio</span>}</dd>
        <dt>precedência</dt>
        <dd className="mono">{res.order + 1}ª</dd>
      </dl>

      <h3 className="section-title">overrides</h3>
      {overrides.length === 0 ? (
        <p className="dim">Nenhum override: a rota encaminha tudo ao upstream.</p>
      ) : (
        <ul className="overrides">
          {overrides.map((o) => {
            const enabled = o.enabled !== false;
            const state = res.state[o.name];
            const active = enabled && (state?.active ?? true);
            return (
              <li key={o.name} className={"override" + (active ? " override--active" : "")}>
                <span className="override__name">
                  {o.name}
                  {o.source?.kind === "learned" ? <span className="dim"> · aprendido</span> : null}
                  {o.source?.kind === "derived" ? <span className="dim"> · derivado</span> : null}
                </span>
                <span className="override__state">{liveSummary(state, enabled, now, at)}</span>
                <span className="override__what">
                  <span className="mono">{overrideSelector(o)}</span> · {overrideSummary(o)}
                </span>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
