import { useEffect, useMemo, useState } from "react";
import { api, type EventStream, type Exchange } from "../api";
import { useResource } from "../hooks";
import { clock, interventionTag, ms } from "../format";
import { CloseIcon } from "./Icons";
import { Panel } from "./Panel";
import { Empty, Failure, Loading } from "./States";

const LIMIT = 100;
const KEEP = 500;

interface TrafficPanelProps {
  stream: EventStream;
  route: string | null;
  onClearRoute: () => void;
}

export function TrafficPanel({ stream, route, onClearRoute }: TrafficPanelProps) {
  // epoch muda quando a listagem precisa ser relida do zero: reconexão
  // (eventos perdidos não são reenviados), limpeza ou troca de backend.
  const [epoch, setEpoch] = useState(0);
  const [page, reload] = useResource(
    (signal) => api.listExchanges(route ? { route } : undefined, { limit: LIMIT }, signal),
    [route, epoch],
  );
  const [live, setLive] = useState<Exchange[]>([]);

  useEffect(() => setLive([]), [page]);

  useEffect(() => {
    const bump = () => setEpoch((n) => n + 1);
    const offHello = stream.on("hello", bump);
    const offHistory = stream.on("history", bump);
    const offEx = stream.on("exchanges", (ev) => {
      const fresh = route ? ev.items.filter((x) => x.route === route) : ev.items;
      if (fresh.length === 0) return;
      // Os eventos chegam da mais antiga para a mais nova; a lista é o inverso.
      setLive((prev) => [...fresh.slice().reverse(), ...prev].slice(0, KEEP));
    });
    return () => {
      offHello();
      offHistory();
      offEx();
    };
  }, [stream, route]);

  const rows = useMemo(() => {
    if (page.kind !== "ready") return [];
    const seen = new Set<string>();
    const out: Exchange[] = [];
    for (const e of [...live, ...page.data.items]) {
      if (seen.has(e.id)) continue;
      seen.add(e.id);
      out.push(e);
    }
    return out.slice(0, KEEP);
  }, [page, live]);

  const tools = (
    <>
      {route ? (
        <span className="chip chip--override">
          rota {route}
          <button type="button" onClick={onClearRoute} aria-label={`Remover o filtro da rota ${route}`}>
            <CloseIcon />
          </button>
        </span>
      ) : null}
      <span className="legend" aria-hidden="true">
        <span>
          <i style={{ background: "var(--time-upstream)" }} /> upstream
        </span>
        <span>
          <i style={{ background: "var(--injected)" }} /> injetado
        </span>
        <span>
          <i style={{ background: "var(--time-gateway)" }} /> gateway
        </span>
      </span>
    </>
  );

  let body;
  if (page.kind === "loading") {
    body = <Loading what="o histórico" />;
  } else if (page.kind === "error") {
    const e = page.error;
    body =
      e.code === "history_disabled" ? (
        <Empty title="Histórico desabilitado">
          <p>{e.message}</p>
          <p>
            A exposição é controlada por <span className="mono">history.expose</span>
            {e.body.env ? (
              <>
                {" "}
                (travada pela variável <span className="mono">{e.body.env}</span>)
              </>
            ) : e.body.file ? (
              <>
                {" "}
                em <span className="mono">{e.body.file}</span>
              </>
            ) : null}
            .
          </p>
        </Empty>
      ) : (
        <Failure what="o histórico" request="GET /api/exchanges" error={e} onRetry={reload} />
      );
  } else if (rows.length === 0) {
    body = page.data.recording ? (
      <Empty title={route ? `Nenhuma troca da rota ${route} ainda` : "Nenhuma troca ainda"}>
        <p>As trocas aparecem aqui assim que uma requisição atravessar a porta de tráfego.</p>
      </Empty>
    ) : (
      <Empty title="Registro desligado">
        <p>
          O histórico está exposto, mas <span className="mono">history.record</span> está desligado: as
          requisições são encaminhadas e nenhuma troca é gravada.
        </p>
      </Empty>
    );
  } else {
    body = <TrafficTable rows={rows} />;
  }

  return (
    <Panel id="traffic" title="tráfego" tools={tools}>
      {body}
    </Panel>
  );
}

function TrafficTable({ rows }: { rows: Exchange[] }) {
  const max = Math.max(1, ...rows.map((r) => r.timing.totalMs));
  return (
    <table className="traffic">
      <colgroup>
        <col style={{ width: "112px" }} />
        <col style={{ width: "64px" }} />
        <col />
        <col style={{ width: "14%" }} />
        <col style={{ width: "52px" }} />
        <col style={{ width: "104px" }} />
        <col style={{ width: "72px" }} />
        <col style={{ width: "22%" }} />
      </colgroup>
      <thead>
        <tr>
          <th scope="col">hora</th>
          <th scope="col">método</th>
          <th scope="col">path</th>
          <th scope="col">rota</th>
          <th scope="col" className="num">
            status
          </th>
          <th scope="col">intervenção</th>
          <th scope="col" className="num">
            total
          </th>
          <th scope="col">tempo</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((e) => {
          const tag = interventionTag(e);
          const status = e.status ? String(e.status) : "—";
          // Vermelho só para o que o gateway fez (queda ou sintetizado); um 500
          // do upstream não é intervenção e fica na cor do texto.
          const fault = e.outcome === "dropped" || e.outcome === "synthesized";
          return (
            <tr key={e.id}>
              <td className="mono dim">{clock(e.start)}</td>
              <td className="mono">{e.method}</td>
              <td className="mono" title={e.path + (e.query ? "?" + e.query : "")}>
                {e.path}
                {e.query ? <span className="dim">?{e.query}</span> : null}
              </td>
              <td className={e.route ? "" : "dim"}>{e.route ?? "sem rota"}</td>
              <td className={"mono num" + (fault ? " status--fault" : "")}>{status}</td>
              <td>{tag ? <span className={`tag tag--${tag.tone}`}>{tag.label}</span> : null}</td>
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
  const label = `upstream ${ms(t.upstreamMs)}, injetado ${ms(t.injectedMs)}, gateway ${ms(t.gatewayMs)}`;
  return (
    <div className="fall" role="img" aria-label={label} title={label}>
      {t.upstreamMs > 0 ? <span className="fall__seg fall__seg--upstream" style={{ width: pct(t.upstreamMs) }} /> : null}
      {t.injectedMs > 0 ? <span className="fall__seg fall__seg--injected" style={{ width: pct(t.injectedMs) }} /> : null}
      {t.gatewayMs > 0 ? <span className="fall__seg fall__seg--gateway" style={{ width: pct(t.gatewayMs) }} /> : null}
    </div>
  );
}
