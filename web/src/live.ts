import { createContext, useContext } from "react";
import type { OverrideLiveState } from "./api";

/**
 * Sem conexão com /api/events, os números vivos (TTL, aplicações, destaque do
 * mapa) param de ser atualizados pelo servidor. Os componentes que os mostram
 * leem este contexto para marcá-los como parados, em vez de parecerem ao vivo.
 */
export const StaleContext = createContext(false);

export function useStale(): boolean {
  return useContext(StaleContext);
}

/** Estado vivo devolvido por uma escrita (PATCH, reset), aplicado sem esperar o evento. */
export type OnOverrideState = (route: string, override: string, enabled: boolean, state: OverrideLiveState) => void;
