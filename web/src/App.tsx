import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  api,
  EventStream,
  type Exchange,
  type OverrideLiveState,
  type OverrideStateList,
  type RouteResource,
  type SettingsPatchResult,
} from "./api";
import { StaleContext } from "./live";
import { EMPTY_FILTER, toExchangeFilter, type ListFilter } from "./exchangeFilter";
import { useConnection, useResource, type Load } from "./hooks";
import { useSelection, type Selection } from "./selection";
import { withLiveState } from "./topology";
import { TopBar } from "./components/TopBar";
import { MapPanel } from "./components/MapPanel";
import { TrafficPanel } from "./components/TrafficPanel";
import { RoutePanel, type ControlView } from "./components/RoutePanel";
import { DocumentPanel, type DocTarget } from "./components/DocumentPanel";
import type { OpenedExchange } from "./components/ExchangeDetail";

const VIEW_KEY = "gateway.painel.controles";

/** A aba lembrada entre sessões: rota ou processo. A troca aberta não sobrevive a um F5. */
function readView(): ControlView {
  // Um link para uma rota (#rota=...) abre os controles dela, não a aba lembrada.
  if (window.location.hash.startsWith("#rota=")) return "route";
  try {
    return window.localStorage.getItem(VIEW_KEY) === "process" ? "process" : "route";
  } catch {
    return "route";
  }
}

export function App() {
  const [stream] = useState(() => new EventStream());
  const connection = useConnection(stream);
  const [status, reloadStatus] = useResource((s) => api.status(s), []);
  // Instante em que a leitura de rotas em curso foi pedida: o estado vivo que
  // chegou depois disso pode ser mais novo que ela e não é descartado.
  const routesAskedAt = useRef(Date.now());
  const [loadedRoutes, reloadRoutes] = useResource((s) => {
    routesAskedAt.current = Date.now();
    return api.listRoutes(s);
  }, []);
  const [upstreams, reloadUpstreams] = useResource((s) => api.upstreams(s), []);
  const [learning, reloadLearning] = useResource((s) => api.learning(s), []);
  const [selection, setSelectionRaw] = useSelection();
  const [docVersion, setDocVersion] = useState(0);
  const [view, setViewRaw] = useState<ControlView>(readView);
  // Troca aberta no detalhe (aba "troca"), com a rota dela para o documento ao lado.
  const [opened, setOpened] = useState<(OpenedExchange & { route?: string }) | null>(null);
  const [listFilter, setListFilter] = useState<ListFilter>(EMPTY_FILTER);
  // Estado vivo dos overrides vindo do evento `overrides` (e das respostas de
  // PATCH e reset). Uma releitura das rotas já traz o estado atual, então só o
  // que chegou antes de ela ser pedida é descartado.
  const [live, setLive] = useState<{ list: OverrideStateList; at: number } | null>(null);
  useEffect(() => setLive((l) => (l && l.at > routesAskedAt.current ? l : null)), [loadedRoutes]);
  const onLiveEvent = useCallback((list: OverrideStateList) => setLive({ list, at: Date.now() }), []);
  const onOverrideState = useCallback(
    (route: string, override: string, enabled: boolean, state: OverrideLiveState) =>
      setLive((l) => {
        const items = (l?.list.items ?? []).filter((it) => !(it.route === route && it.override === override));
        items.push({ ...state, route, override, enabled });
        return { list: { now: new Date().toISOString(), items }, at: Date.now() };
      }),
    [],
  );
  // A porta de administração mudou por uma escrita desta aba: esta origem
  // deixa de responder, então o fluxo para e a barra aponta a porta nova.
  const [movedTo, setMovedTo] = useState<number | null>(null);

  const setView = useCallback((v: ControlView) => {
    setViewRaw(v);
    if (v === "exchange") return;
    try {
      window.localStorage.setItem(VIEW_KEY, v);
    } catch {
      // só conveniência: sem armazenamento, a aba volta a "rota" no F5
    }
  }, []);

  // Selecionar uma rota no mapa traz os controles dela para a frente.
  const setSelection = useCallback(
    (s: Selection) => {
      setSelectionRaw(s);
      if (s?.kind === "route") setView("route");
    },
    [setSelectionRaw, setView],
  );

  useEffect(() => {
    stream.start();
    const all = () => {
      reloadStatus();
      reloadRoutes();
      reloadUpstreams();
      reloadLearning();
      setDocVersion((v) => v + 1);
    };
    // O evento upstreams só sai quando o status muda; as contagens recentes
    // (falhas em tentativas) andam a cada troca, então são relidas junto com
    // as trocas novas, no máximo a cada dois segundos.
    let countsTimer: number | undefined;
    const refreshCounts = () => {
      if (countsTimer !== undefined) return;
      countsTimer = window.setTimeout(() => {
        countsTimer = undefined;
        reloadUpstreams();
      }, 2000);
    };
    const offs = [
      stream.on("exchanges", refreshCounts),
      // Ao (re)conectar, tudo é relido: o servidor não reenvia eventos perdidos.
      stream.on("hello", all),
      // Escrita, recarga ou aprendizado: rotas, upstreams, estado do processo
      // e documentos podem ter mudado juntos.
      stream.on("config", all),
      stream.on("overrides", onLiveEvent),
      stream.on("upstreams", reloadUpstreams),
      stream.on("history", reloadStatus),
    ];
    return () => {
      for (const off of offs) off();
      window.clearTimeout(countsTimer);
      stream.stop();
    };
  }, [stream, reloadStatus, reloadRoutes, reloadUpstreams, reloadLearning, onLiveEvent]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      const t = e.target as HTMLElement | null;
      if (t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return;
      if (t?.closest("[role=alertdialog]")) return;
      setSelection(null);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [setSelection]);

  const routes: Load<RouteResource[]> = useMemo(
    () =>
      loadedRoutes.kind === "ready"
        ? { kind: "ready", data: withLiveState(loadedRoutes.data, live?.list ?? null) }
        : loadedRoutes,
    [loadedRoutes, live],
  );

  const selectedRouteName = selection?.kind === "route" ? selection.name : null;
  const exchangeFilter = useMemo(() => toExchangeFilter(selection, listFilter), [selection, listFilter]);

  const openExchange = useCallback(
    (ex: OpenedExchange & { route?: string }) => {
      setOpened({ ...ex, route: ex.route ?? ex.data?.route });
      setView("exchange");
    },
    [setView],
  );
  const closeExchange = useCallback(() => {
    setOpened(null);
    setViewRaw((v) => (v === "exchange" ? readView() : v));
  }, []);
  // Seletor de rota da lista de tráfego: filtra como o mapa, sem trocar a aba do detalhe.
  const filterRoute = useCallback(
    (name: string | null) => setSelectionRaw(name ? { kind: "route", name } : null),
    [setSelectionRaw],
  );
  const exchangeRoute = opened ? (opened.route ?? opened.data?.route) : undefined;
  const selectedRoute =
    routes.kind === "ready" && selectedRouteName
      ? routes.data.find((r) => r.route.name === selectedRouteName)
      : undefined;

  const docTarget: DocTarget =
    view === "process"
      ? { kind: "process" }
      : view === "exchange"
        ? exchangeRoute
          ? {
              kind: "route",
              name: exchangeRoute,
              route: routes.kind === "ready" ? routes.data.find((r) => r.route.name === exchangeRoute) : undefined,
            }
          : null
        : selectedRouteName
        ? { kind: "route", name: selectedRouteName, route: selectedRoute }
        : null;

  const currentAdmin = status.kind === "ready" ? status.data.ports.admin : undefined;
  const onSettings = useCallback(
    (r: SettingsPatchResult) => {
      const v = r.settings.values.find((x) => x.key === "ports.admin")?.value;
      if (typeof v === "number" && currentAdmin !== undefined && v !== currentAdmin) {
        setMovedTo(v);
        stream.stop();
      }
    },
    [currentAdmin, stream],
  );

  const toggleLearning = useCallback(
    (enabled: boolean) => api.setLearning(enabled).then(() => {
      reloadLearning();
      reloadStatus();
    }),
    [reloadLearning, reloadStatus],
  );

  return (
    <StaleContext.Provider value={connection.kind !== "open"}>
    <div className="console">
      <TopBar
        movedTo={movedTo}
        connection={connection}
        status={status}
        onRetry={stream.retryNow}
        learning={learning}
        onLearning={toggleLearning}
      />
      <main className="columns">
        <div className="column column--left">
          <MapPanel
            routes={routes}
            upstreams={upstreams}
            trafficPort={status.kind === "ready" ? status.data.ports.traffic : undefined}
            selection={selection}
            onSelect={setSelection}
            onRetry={reloadRoutes}
          />
          <TrafficPanel
            stream={stream}
            connection={connection}
            status={status}
            routes={routes}
            selection={selection}
            onRoute={filterRoute}
            listFilter={listFilter}
            onListFilter={setListFilter}
            filter={exchangeFilter}
            openId={opened?.id ?? null}
            onOpen={(ex: Exchange) => openExchange({ id: ex.id, route: ex.route })}
          />
        </div>
        <div className={"column column--right" + (view === "exchange" ? " column--detail" : "")}>
          <RoutePanel
            view={view}
            onView={setView}
            routes={routes}
            selection={selection}
            onSelect={setSelection}
            onRetry={reloadRoutes}
            learning={learning}
            onShowExchange={(id) => openExchange({ id, route: selectedRouteName ?? undefined })}
            version={docVersion}
            exchange={opened}
            onOpenExchange={openExchange}
            onCloseExchange={closeExchange}
            exchangeFilter={exchangeFilter}
            upstreams={upstreams}
            onSettings={onSettings}
            onOverrideState={onOverrideState}
            guardScope={status.kind === "ready" ? status.data.routesDir : ""}
          />
          <DocumentPanel target={docTarget} version={docVersion} onSettings={onSettings} />
        </div>
      </main>
      <datalist id="http-methods">
        {["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"].map((m) => (
          <option key={m} value={m} />
        ))}
      </datalist>
    </div>
    </StaleContext.Provider>
  );
}
