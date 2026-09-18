import { useEffect, useState } from "react";
import { api, EventStream } from "./api";
import { useConnection, useResource } from "./hooks";
import { TopBar } from "./components/TopBar";
import { MapPanel } from "./components/MapPanel";
import { TrafficPanel } from "./components/TrafficPanel";
import { RoutePanel } from "./components/RoutePanel";
import { DocumentPanel } from "./components/DocumentPanel";

function readSelection(): string | null {
  const m = /^#rota=(.+)$/.exec(window.location.hash);
  return m?.[1] ? decodeURIComponent(m[1]) : null;
}

export function App() {
  const [stream] = useState(() => new EventStream());
  const connection = useConnection(stream);
  const [status, reloadStatus] = useResource((s) => api.status(s), []);
  const [routes, reloadRoutes] = useResource((s) => api.listRoutes(s), []);
  const [upstreams, reloadUpstreams] = useResource((s) => api.upstreams(s), []);
  // A seleção vive no fragmento (#rota=payments) para sobreviver a um F5.
  const [selected, setSelected] = useState<string | null>(readSelection);
  useEffect(() => {
    const hash = selected ? "#rota=" + encodeURIComponent(selected) : "";
    if (window.location.hash !== hash) {
      history.replaceState(null, "", window.location.pathname + window.location.search + hash);
    }
  }, [selected]);
  const [docVersion, setDocVersion] = useState(0);

  useEffect(() => {
    stream.start();
    const offs = [
      // Ao (re)conectar, tudo é relido: o servidor não reenvia eventos perdidos.
      stream.on("hello", () => {
        reloadStatus();
        reloadRoutes();
        reloadUpstreams();
        setDocVersion((v) => v + 1);
      }),
      stream.on("config", () => {
        reloadStatus();
        reloadRoutes();
        setDocVersion((v) => v + 1);
      }),
      stream.on("overrides", reloadRoutes),
      stream.on("upstreams", reloadUpstreams),
      stream.on("history", reloadStatus),
    ];
    return () => {
      for (const off of offs) off();
      stream.stop();
    };
  }, [stream, reloadStatus, reloadRoutes, reloadUpstreams]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setSelected(null);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const selectedRoute =
    routes.kind === "ready" && selected ? routes.data.find((r) => r.route.name === selected) : undefined;

  return (
    <div className="console">
      <TopBar connection={connection} status={status} onRetry={stream.retryNow} />
      <main className="columns">
        <div className="column column--left">
          <MapPanel
            routes={routes}
            upstreams={upstreams}
            selected={selected}
            onSelect={setSelected}
            onRetry={reloadRoutes}
          />
          <TrafficPanel stream={stream} route={selected} onClearRoute={() => setSelected(null)} />
        </div>
        <div className="column column--right">
          <RoutePanel routes={routes} selected={selected} onRetry={reloadRoutes} />
          <DocumentPanel selected={selected} route={selectedRoute} version={docVersion} />
        </div>
      </main>
    </div>
  );
}
