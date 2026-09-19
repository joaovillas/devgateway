// Reading the configuration as a list of services: what each route does with
// the traffic right now, where your app comes in, where the gateway sends it
// and how the live state of the overrides arrives over SSE.
import type { Override, OverrideLiveState, OverrideStateList, RouteResource, UpstreamHealth } from "./api";
import { leadingEffect, neverApplies, type Effect } from "./effects";
import { latency, percent } from "./format";

export type InterventionKind = "synth" | "drop" | "delay";

export interface RouteIntervention {
  /** The effect of the highest-precedence rule among the active ones. */
  kind: InterventionKind;
  /** Short, for the badge in the list: "503", "drop", "+2s". */
  short: string;
  /** Spelled out, for screen readers and tooltips: "responds 503 on 30% of calls (rule flaky)". */
  long: string;
  /** How often that effect holds, from 0 to 1. */
  chance: number;
  /** How many active rules the service has in total. */
  count: number;
}

/**
 * A rule is intervening right now when it is on, the server reports it as
 * active, at least one of its effects can still be drawn, and the TTL has not
 * run out since the last reading (`at`). The server announces expiry by
 * event; the local arithmetic only keeps the highlight from outliving the
 * clock between one event and the next.
 */
export function isActive(o: Override, s: OverrideLiveState | undefined, now: number, at: number): boolean {
  if (o.enabled === false) return false;
  // Every effect at 0%: the rule says nothing that can ever happen.
  if (neverApplies(o)) return false;
  if (!s) return true;
  if (!s.active || s.expired) return false;
  if (s.ttlRemainingMs !== null && s.ttlRemainingMs - (now - at) <= 0) return false;
  if (s.maxApplications !== null && s.applications >= s.maxApplications) return false;
  return true;
}

function effectText(o: Override, e: Effect): { kind: InterventionKind; short: string; long: string } {
  switch (e.kind) {
    case "drop":
      return { kind: "drop", short: "drop", long: "drops the connection" };
    case "respond": {
      const st = o.respond?.status ?? 200;
      return { kind: "synth", short: String(st), long: `responds ${st}` };
    }
    case "latency": {
      const d = latency(o.latency!);
      return { kind: "delay", short: "+" + d, long: `delays ${d}` };
    }
  }
}

export function routeIntervention(res: RouteResource, now: number, at: number): RouteIntervention | null {
  const active = (res.route.overrides ?? []).filter((o) => isActive(o, res.state[o.name], now, at));
  const first = active[0];
  if (!first) return null;
  const lead = leadingEffect(first);
  const e = lead ? effectText(first, lead) : { kind: "delay" as const, short: "no effect", long: "no effect declared" };
  const chance = lead?.chance ?? 0;
  const others = active.length > 1 ? ` and ${active.length - 1} more ${active.length === 2 ? "rule" : "rules"}` : "";
  return {
    ...e,
    chance,
    count: active.length,
    long: `${e.long} on ${percent(chance)} of calls (rule ${first.name}${others})`,
  };
}

/** Some override in the list has a TTL running down: the badge needs a clock. */
export function hasTicking(routes: RouteResource[]): boolean {
  return routes.some((r) => Object.values(r.state ?? {}).some((s) => s.ttlRemainingMs != null && s.ttlRemainingMs > 0));
}

/**
 * Applies the live state received in the `overrides` event over the routes
 * read by REST, without re-reading the routes on every application.
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

/** The service's entry: the host and/or path matched, or "" when it matches any request. */
export function entryText(r: RouteResource): string {
  const m = r.route.match;
  return [m.host, m.path].filter(Boolean).join(" ");
}

/** The destination as the developer recognizes it: host:port, without the scheme. */
export function destinationText(url: string): string {
  try {
    const u = new URL(url);
    return u.host + (u.pathname !== "/" ? u.pathname : "");
  } catch {
    return url.replace(/^https?:\/\//, "");
  }
}

/** The known destinations (declared by the services or with health recorded), without repetition. */
export function knownDestinations(routes: RouteResource[], health: UpstreamHealth[]): string[] {
  const out = new Set<string>();
  for (const r of routes) if (r.route.upstream) out.add(r.route.upstream);
  for (const h of health) out.add(h.upstream);
  return [...out];
}

/** The destination's health in words: short for the row, long for the tooltip and the screen reader. */
export function healthText(status: UpstreamHealth["status"], h: UpstreamHealth | undefined): { short: string; long: string } {
  if (status === "down") {
    return {
      short: "down",
      long: `down, ${h?.recent.failures ?? 0} of ${h?.recent.attempts ?? 0} recent attempts failed${h?.lastError ? `: ${h.lastError}` : ""}`,
    };
  }
  if (status === "up") {
    const f = h?.recent.failures ?? 0;
    return {
      short: f > 0 ? `${f}/${h?.recent.attempts} failures` : "up",
      long: f > 0 ? `up, ${f} of ${h?.recent.attempts} recent attempts failed` : "up, answering",
    };
  }
  return { short: "no attempts", long: "no request forwarded to it recently" };
}

/** A service as both views (map and list) show it. */
export interface ServiceRow {
  res: RouteResource;
  name: string;
  entry: string;
  upstream: string | undefined;
  status: UpstreamHealth["status"];
  health: UpstreamHealth | undefined;
  intervention: RouteIntervention | null;
  /** Lowercased text the search looks into: name, entry and destination. */
  haystack: string;
}

/** The services with the state of their destination and the rule active right now, in precedence order. */
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

/** Search terms, separated by spaces; a service matches when it contains all of them. */
export function searchTerms(query: string): string[] {
  return query.trim().toLowerCase().split(/\s+/).filter(Boolean);
}

export function filterRows(rows: ServiceRow[], terms: string[]): ServiceRow[] {
  return terms.length ? rows.filter((r) => terms.every((t) => r.haystack.includes(t))) : rows;
}
