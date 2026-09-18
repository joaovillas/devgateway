import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { ApiError, type ConnectionState, type EventStream } from "./api";

/** Estado de um recurso carregado da API. Nada de dado inventado no meio. */
export type Load<T> =
  | { kind: "loading" }
  | { kind: "ready"; data: T }
  | { kind: "error"; error: ApiError };

export function toApiError(e: unknown): ApiError {
  if (e instanceof ApiError) return e;
  return new ApiError(0, { error: "client", message: e instanceof Error ? e.message : String(e) });
}

/**
 * Carrega um recurso e recarrega quando `key` muda ou quando `reload()` é
 * chamado. Uma recarga mantém o dado anterior à vista até a resposta chegar,
 * para a tela não piscar em "carregando" a cada evento.
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
    // Chave nova: volta a "carregando", porque o dado anterior é de outra coisa.
    if (lastKey.current !== keyString) {
      lastKey.current = keyString;
      setState({ kind: "loading" });
    }
    const ctl = new AbortController();
    fetcherRef.current(ctl.signal).then(
      (data) => setState({ kind: "ready", data }),
      (e: unknown) => {
        if (ctl.signal.aborted) return;
        setState({ kind: "error", error: toApiError(e) });
      },
    );
    return () => ctl.abort();
  }, [keyString, tick]);

  const reload = useCallback(() => setTick((t) => t + 1), []);
  return [state, reload];
}

export function useConnection(stream: EventStream): ConnectionState {
  return useSyncExternalStore(
    (cb) => stream.onState(cb),
    () => stream.getState(),
  );
}

/** Relógio que só bate enquanto `active`, para contagens regressivas. */
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
