import type { Exchange, ExchangeFilter } from "./api";
import type { Selection } from "./selection";

/**
 * Filters of the traffic list beyond the selection in the services list (which
 * gives the route or the upstream). The same values go to GET /api/exchanges, to the
 * item-by-item navigation (older/newer) and to the live stream, which is filtered here.
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

/** The filter in the API's format (exchange.Filter), adding in the selection from the services list. */
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

/** The same rule as the server's, for the exchanges that arrive over SSE. */
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
 * There was an intervention: the same criterion as the list's tag
 * (interventionTag), which takes the outcome as proof when the summary does
 * not carry `interventions`.
 */
export function intervened(e: Exchange): boolean {
  return (e.interventions ?? []).length > 0 || e.outcome === "synthesized" || e.outcome === "dropped";
}

/** Short text for the active filter, for messages like "there are no older exchanges with …". */
export function describeFilter(f: ExchangeFilter): string {
  const parts: string[] = [];
  if (f.route) parts.push(`service ${f.route}`);
  if (f.upstream) parts.push(`destination ${f.upstream.replace(/^https?:\/\//, "")}`);
  if (f.method) parts.push(f.method);
  if (f.path) parts.push(`path containing “${f.path}”`);
  if (f.statusMin !== undefined) parts.push(`status ${String(f.statusMin)[0]}xx`);
  if (f.intervened !== undefined) parts.push(f.intervened ? "with intervention" : "without intervention");
  return parts.join(", ");
}
