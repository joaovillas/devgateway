import type { Exchange, ExchangeFilter } from "./api";
import type { Selection } from "./selection";

/**
 * Filtros da lista de tráfego além da seleção do mapa (que dá a rota ou o
 * upstream). Os mesmos valores vão para GET /api/exchanges, para a navegação
 * item a item (older/newer) e para o fluxo ao vivo, que é filtrado aqui.
 */
export type StatusClass = "all" | "2xx" | "3xx" | "4xx" | "5xx";
export type InterventionFilter = "all" | "yes" | "no";

export interface ListFilter {
  method: string;
  path: string;
  status: StatusClass;
  intervened: InterventionFilter;
}

export const EMPTY_FILTER: ListFilter = { method: "", path: "", status: "all", intervened: "all" };

export function isFiltering(f: ListFilter): boolean {
  return f.method !== "" || f.path.trim() !== "" || f.status !== "all" || f.intervened !== "all";
}

const STATUS_RANGE: Record<Exclude<StatusClass, "all">, [number, number]> = {
  "2xx": [200, 299],
  "3xx": [300, 399],
  "4xx": [400, 499],
  "5xx": [500, 599],
};

/** O filtro no formato da API (exchange.Filter), somando a seleção do mapa. */
export function toExchangeFilter(sel: Selection, f: ListFilter): ExchangeFilter {
  const out: ExchangeFilter = {};
  if (sel?.kind === "route") out.route = sel.name;
  if (sel?.kind === "upstream") out.upstream = sel.name;
  if (f.method) out.method = f.method;
  if (f.path.trim()) out.path = f.path.trim();
  if (f.status !== "all") [out.statusMin, out.statusMax] = STATUS_RANGE[f.status];
  if (f.intervened !== "all") out.intervened = f.intervened === "yes";
  return out;
}

/** A mesma regra do servidor, para as trocas que chegam pelo SSE. */
export function matches(e: Exchange, f: ExchangeFilter): boolean {
  if (f.route !== undefined && e.route !== f.route) return false;
  if (f.upstream !== undefined && e.upstream !== f.upstream) return false;
  if (f.override !== undefined && e.override !== f.override) return false;
  if (f.method !== undefined && e.method.toUpperCase() !== f.method.toUpperCase()) return false;
  if (f.path !== undefined && !e.path.includes(f.path)) return false;
  const status = e.status ?? 0;
  if (f.statusMin !== undefined && status < f.statusMin) return false;
  if (f.statusMax !== undefined && status > f.statusMax) return false;
  if (f.intervened !== undefined && intervened(e) !== f.intervened) return false;
  return true;
}

/**
 * Houve intervenção: o mesmo critério da etiqueta da lista (interventionTag),
 * que aceita o resultado como prova quando o resumo não traz `interventions`.
 */
export function intervened(e: Exchange): boolean {
  return (e.interventions ?? []).length > 0 || e.outcome === "synthesized" || e.outcome === "dropped";
}

/** Texto curto do filtro ativo, para mensagens como "não há trocas mais antigas com …". */
export function describeFilter(f: ExchangeFilter): string {
  const parts: string[] = [];
  if (f.route) parts.push(`rota ${f.route}`);
  if (f.upstream) parts.push(`upstream ${f.upstream.replace(/^https?:\/\//, "")}`);
  if (f.method) parts.push(f.method);
  if (f.path) parts.push(`path com “${f.path}”`);
  if (f.statusMin !== undefined) parts.push(`status ${String(f.statusMin)[0]}xx`);
  if (f.intervened !== undefined) parts.push(f.intervened ? "com intervenção" : "sem intervenção");
  return parts.join(", ");
}
