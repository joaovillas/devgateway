import { createContext, useContext } from "react";
import type { OverrideLiveState } from "./api";

/**
 * With no connection to /api/events, the live numbers (TTL, applications, the
 * map highlight) stop being updated by the server. The components that show
 * them read this context to mark them as stalled instead of looking live.
 */
export const StaleContext = createContext(false);

export function useStale(): boolean {
  return useContext(StaleContext);
}

/** Live state returned by a write (PATCH, reset), applied without waiting for the event. */
export type OnOverrideState = (route: string, override: string, enabled: boolean, state: OverrideLiveState) => void;
