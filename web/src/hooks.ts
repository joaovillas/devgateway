import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { ApiError, type ConnectionState, type EventStream } from "./api";

/** The state of a resource loaded from the API. No invented data in between. */
export type Load<T> =
  | { kind: "loading" }
  | { kind: "ready"; data: T }
  | { kind: "error"; error: ApiError };

export function toApiError(e: unknown): ApiError {
  if (e instanceof ApiError) return e;
  return new ApiError(0, { error: "client", message: e instanceof Error ? e.message : String(e) });
}

/**
 * Loads a resource and reloads it when `key` changes or when `reload()` is
 * called. A reload keeps the previous data in view until the response arrives,
 * so that the screen does not flash "loading" on every event.
 */
export function useResource<T>(
  fetcher: (signal: AbortSignal) => Promise<T>,
  key: unknown[],
): [Load<T>, () => void] {
  const [state, setState] = useState<Load<T>>({ kind: "loading" });
  const [tick, setTick] = useState(0);
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;
  const keyString = JSON.stringify(key);
  const lastKey = useRef(keyString);

  useEffect(() => {
    // A new key: back to "loading", because the previous data is about something else.
    if (lastKey.current !== keyString) {
      lastKey.current = keyString;
      setState({ kind: "loading" });
    }
    const ctl = new AbortController();
    fetcherRef.current(ctl.signal).then(
      (data) => {
        if (!ctl.signal.aborted) setState({ kind: "ready", data });
      },
      (e: unknown) => {
        if (ctl.signal.aborted) return;
        const error = toApiError(e);
        // A reload with no response from the admin port (the process is down):
        // the previous data stays in view, and the bar and the panels already
        // say the panel stopped updating. Reconnecting re-reads everything.
        setState((prev) => (prev.kind === "ready" && error.code === "network" ? prev : { kind: "error", error }));
      },
    );
    return () => ctl.abort();
  }, [keyString, tick]);

  const reload = useCallback(() => setTick((t) => t + 1), []);
  // On the render where the key has just changed, the state is still the one
  // from the previous key (the effect that resets it runs afterwards): it does
  // not hand back data about something else.
  return [lastKey.current === keyString ? state : { kind: "loading" }, reload];
}

export function useConnection(stream: EventStream): ConnectionState {
  return useSyncExternalStore(
    (cb) => stream.onState(cb),
    () => stream.getState(),
  );
}

/** A clock that only ticks while `active`, for countdowns. */
export function useNow(active: boolean, everyMs = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    setNow(Date.now());
    const id = window.setInterval(() => setNow(Date.now()), everyMs);
    return () => window.clearInterval(id);
  }, [active, everyMs]);
  return now;
}
