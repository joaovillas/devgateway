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
import { readMode, storeMode, type DetailMode } from "./mode";
import { useSelection, type Selection } from "./selection";
import { withLiveState } from "./services";
import { TopBar } from "./components/TopBar";
import { ServicesPanel } from "./components/ServicesPanel";
import { TrafficPanel } from "./components/TrafficPanel";
import { RoutePanel, type ControlView } from "./components/RoutePanel";
import type { OpenedExchange } from "./components/ExchangeDetail";

const VIEW_KEY = "gateway.panel.controls";

/** The tab remembered between sessions: service or process. The open exchange does not survive an F5. */
function readView(): ControlView {
  // A link to a service (#route=...) opens its controls, not the remembered tab.
  if (window.location.hash.startsWith("#route=")) return "route";
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
  // The instant the routes read in flight was asked for: live state that
  // arrived after that may be newer than it, and so is not discarded.
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
  // Simple (the default) or advanced, remembered between visits.
  const [mode, setModeRaw] = useState<DetailMode>(readMode);
  const setMode = useCallback((m: DetailMode) => {
    setModeRaw(m);
    storeMode(m);
  }, []);
  // The exchange open in the detail (the "exchange" tab), with its route.
  const [opened, setOpened] = useState<(OpenedExchange & { route?: string }) | null>(null);
  const [listFilter, setListFilter] = useState<ListFilter>(EMPTY_FILTER);
  // The new-service form open in the right panel, with or without a selection.
  const [creating, setCreating] = useState(false);
  // Live override state coming from the `overrides` event (and from the PATCH
  // and reset responses). A re-read of the routes already brings the current
  // state, so only what arrived before it was asked for is discarded.
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
  // The admin port changed because of a write from this tab: this origin
  // stops answering, so the stream stops and the bar points at the new port.
  const [movedTo, setMovedTo] = useState<number | null>(null);

  const setView = useCallback((v: ControlView) => {
    setViewRaw(v);
    if (v === "exchange") return;
    try {
      window.localStorage.setItem(VIEW_KEY, v);
    } catch {
      // only a convenience: with no storage, the tab goes back to "route" on F5
    }
  }, []);

  // Selecting a service on the map or in the list brings its controls to the front
  // and closes the new-service form if one was open.
  const setSelection = useCallback(
    (s: Selection) => {
      setSelectionRaw(s);
      if (s?.kind === "route") {
        setCreating(false);
        setView("route");
      }
    },
    [setSelectionRaw, setView],
  );
  const startCreate = useCallback(() => {
    setCreating(true);
    setView("route");
  }, [setView]);
  const cancelCreate = useCallback(() => setCreating(false), []);

  useEffect(() => {
    stream.start();
    const all = () => {
      reloadStatus();
      reloadRoutes();
      reloadUpstreams();
      reloadLearning();
      setDocVersion((v) => v + 1);
    };
    // The upstreams event only goes out when the status changes; the recent
    // counts (failures out of attempts) move on every exchange, so they are
    // re-read together with the new exchanges, at most every two seconds.
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
      // On (re)connecting everything is re-read: the server does not resend missed events.
      stream.on("hello", all),
      // A write, a reload or learning: routes, upstreams, process state and
      // documents may have changed together.
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
  // Service created: re-read the list (the config event would arrive too) and select it.
  const onCreated = useCallback(
    (name: string) => {
      reloadRoutes();
      setSelection({ kind: "route", name });
    },
    [reloadRoutes, setSelection],
  );
  // Service picker in the traffic list: filters like the services panel, without switching the detail tab.
  const filterRoute = useCallback(
    (name: string | null) => setSelectionRaw(name ? { kind: "route", name } : null),
    [setSelectionRaw],
  );
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
          <ServicesPanel
            routes={routes}
            upstreams={upstreams}
            trafficPort={status.kind === "ready" ? status.data.ports.traffic : undefined}
            selection={selection}
            onSelect={setSelection}
            onRetry={reloadRoutes}
            creating={creating && view === "route"}
            onCreate={startCreate}
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
        <div className={"column column--right"}>
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
            creating={creating}
            onCreate={startCreate}
            onCreated={onCreated}
            onCancelCreate={cancelCreate}
            mode={mode}
            onMode={setMode}
            trafficPort={status.kind === "ready" ? status.data.ports.traffic : undefined}
          />
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
