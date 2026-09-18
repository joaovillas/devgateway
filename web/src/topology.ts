// Leitura da configuração como topologia: o que cada rota faz agora com o
// tráfego e como o estado vivo dos overrides chega pelo SSE.
import type { Override, OverrideLiveState, OverrideStateList, RouteResource } from "./api";
import { latency, percent } from "./format";

export type InterventionKind = "synth" | "drop" | "delay";

export interface RouteIntervention {
  /** O efeito do override de maior precedência entre os ativos. */
  kind: InterventionKind;
  /** Curto, para o nó do mapa: "503", "queda", "+2s". */
  short: string;
  /** Por extenso, para leitor de tela e dica: "sintetiza 503 em 30% (flaky)". */
  long: string;
  probability: number;
  /** Quantos overrides ativos a rota tem ao todo. */
  count: number;
}

/**
 * Um override intervém agora quando está ligado, o servidor o dá como ativo e
 * o TTL não se esgotou desde a última leitura (`at`). O servidor avisa a
 * expiração por evento; a conta local só evita que o destaque sobreviva ao
 * relógio entre um evento e outro.
 */
export function isActive(o: Override, s: OverrideLiveState | undefined, now: number, at: number): boolean {
  if (o.enabled === false) return false;
  if (!s) return true;
  if (!s.active || s.expired) return false;
  if (s.ttlRemainingMs !== null && s.ttlRemainingMs - (now - at) <= 0) return false;
  if (s.maxApplications !== null && s.applications >= s.maxApplications) return false;
  return true;
}

function effect(o: Override): { kind: InterventionKind; short: string; long: string } {
  if (o.drop) return { kind: "drop", short: "queda", long: "derruba a conexão" };
  if (o.respond) {
    const st = o.respond.status ?? 200;
    const extra = o.latency ? ` após ${latency(o.latency)}` : "";
    return { kind: "synth", short: String(st), long: `sintetiza ${st}${extra}` };
  }
  if (o.latency) return { kind: "delay", short: "+" + latency(o.latency), long: `atrasa ${latency(o.latency)}` };
  return { kind: "delay", short: "sem efeito", long: "sem efeito declarado" };
}

export function routeIntervention(res: RouteResource, now: number, at: number): RouteIntervention | null {
  const active = (res.route.overrides ?? []).filter((o) => isActive(o, res.state[o.name], now, at));
  const first = active[0];
  if (!first) return null;
  const e = effect(first);
  const p = first.probability ?? 1;
  const others = active.length > 1 ? ` e mais ${active.length - 1} ${active.length === 2 ? "override" : "overrides"}` : "";
  return {
    ...e,
    probability: p,
    count: active.length,
    long: `${e.long} em ${percent(p)} (override ${first.name}${others})`,
  };
}

/** Algum override da lista tem TTL correndo: o mapa precisa de relógio. */
export function hasTicking(routes: RouteResource[]): boolean {
  return routes.some((r) => Object.values(r.state ?? {}).some((s) => s.ttlRemainingMs != null && s.ttlRemainingMs > 0));
}

/**
 * Aplica o estado vivo recebido pelo evento `overrides` sobre as rotas lidas
 * por REST, sem reler as rotas a cada aplicação.
 */
export function withLiveState(routes: RouteResource[], live: OverrideStateList | null): RouteResource[] {
  if (!live) return routes;
  const byRoute = new Map<string, Record<string, OverrideLiveState>>();
  for (const it of live.items) {
    const { route, override, enabled: _enabled, ...state } = it;
    let m = byRoute.get(route);
    if (!m) byRoute.set(route, (m = {}));
    m[override] = state;
  }
  return routes.map((r) => {
    const s = byRoute.get(r.route.name);
    return s ? { ...r, state: { ...r.state, ...s } } : r;
  });
}
