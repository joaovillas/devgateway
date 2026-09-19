// Leitura da configuração como lista de serviços: o que cada rota faz agora
// com o tráfego, por onde o app entra, para onde o gateway manda e como o
// estado vivo dos overrides chega pelo SSE.
import type { Override, OverrideLiveState, OverrideStateList, RouteResource, UpstreamHealth } from "./api";
import { latency, percent } from "./format";

export type InterventionKind = "synth" | "drop" | "delay";

export interface RouteIntervention {
  /** O efeito do override de maior precedência entre os ativos. */
  kind: InterventionKind;
  /** Curto, para o selo da lista: "503", "queda", "+2s". */
  short: string;
  /** Por extenso, para leitor de tela e dica: "responde 503 em 30% (regra flaky)". */
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
    return { kind: "synth", short: String(st), long: `responde ${st}${extra}` };
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
  const others = active.length > 1 ? ` e mais ${active.length - 1} ${active.length === 2 ? "regra" : "regras"}` : "";
  return {
    ...e,
    probability: p,
    count: active.length,
    long: `${e.long} em ${percent(p)} (regra ${first.name}${others})`,
  };
}

/** Algum override da lista tem TTL correndo: o selo precisa de relógio. */
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

/** A entrada do serviço: host e/ou path casados, ou "" quando casa qualquer requisição. */
export function entryText(r: RouteResource): string {
  const m = r.route.match;
  return [m.host, m.path].filter(Boolean).join(" ");
}

/** O destino como o dev o reconhece: host:porta, sem esquema. */
export function destinationText(url: string): string {
  try {
    const u = new URL(url);
    return u.host + (u.pathname !== "/" ? u.pathname : "");
  } catch {
    return url.replace(/^https?:\/\//, "");
  }
}

/** Os destinos conhecidos (declarados pelos serviços ou com saúde registrada), sem repetição. */
export function knownDestinations(routes: RouteResource[], health: UpstreamHealth[]): string[] {
  const out = new Set<string>();
  for (const r of routes) if (r.route.upstream) out.add(r.route.upstream);
  for (const h of health) out.add(h.upstream);
  return [...out];
}

/** Saúde do destino em palavras: curta para a linha, longa para a dica e o leitor de tela. */
export function healthText(status: UpstreamHealth["status"], h: UpstreamHealth | undefined): { short: string; long: string } {
  if (status === "down") {
    return {
      short: "fora",
      long: `fora do ar, ${h?.recent.failures ?? 0} de ${h?.recent.attempts ?? 0} tentativas recentes falharam${h?.lastError ? `: ${h.lastError}` : ""}`,
    };
  }
  if (status === "up") {
    const f = h?.recent.failures ?? 0;
    return {
      short: f > 0 ? `${f}/${h?.recent.attempts} falhas` : "no ar",
      long: f > 0 ? `no ar, ${f} de ${h?.recent.attempts} tentativas recentes falharam` : "no ar, respondendo",
    };
  }
  return { short: "sem tentativas", long: "nenhuma requisição encaminhada a ele recentemente" };
}

/** Um serviço como as duas visões (mapa e lista) o mostram. */
export interface ServiceRow {
  res: RouteResource;
  name: string;
  entry: string;
  upstream: string | undefined;
  status: UpstreamHealth["status"];
  health: UpstreamHealth | undefined;
  intervention: RouteIntervention | null;
  /** Texto em minúsculas onde a busca procura: nome, entrada e destino. */
  haystack: string;
}

/** Os serviços com o estado do destino e a regra ativa agora, na ordem de precedência. */
export function serviceRows(routes: RouteResource[], health: UpstreamHealth[], now: number, at: number): ServiceRow[] {
  const byUrl = new Map(health.map((h) => [h.upstream, h]));
  return routes.map((res) => {
    const upstream = res.route.upstream || undefined;
    const h = upstream ? byUrl.get(upstream) : undefined;
    const entry = entryText(res);
    return {
      res,
      name: res.route.name,
      entry,
      upstream,
      status: h?.status ?? "unknown",
      health: h,
      intervention: routeIntervention(res, now, at),
      haystack: [res.route.name, entry, upstream ?? ""].join(" ").toLowerCase(),
    };
  });
}

/** Termos da busca, separados por espaço; um serviço casa quando contém todos. */
export function searchTerms(query: string): string[] {
  return query.trim().toLowerCase().split(/\s+/).filter(Boolean);
}

export function filterRows(rows: ServiceRow[], terms: string[]): ServiceRow[] {
  return terms.length ? rows.filter((r) => terms.every((t) => r.haystack.includes(t))) : rows;
}
