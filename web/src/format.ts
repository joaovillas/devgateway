import type { Exchange, Latency, Override, OverrideLiveState } from "./api";

export function clock(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const p = (n: number, w = 2) => String(n).padStart(w, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(), 3)}`;
}

export function ms(v: number): string {
  if (v >= 10_000) return (v / 1000).toFixed(1) + " s";
  if (v >= 1000) return (v / 1000).toFixed(2) + " s";
  if (v >= 10) return Math.round(v) + " ms";
  return v.toFixed(1) + " ms";
}

export function seconds(msLeft: number): string {
  const s = Math.max(0, Math.ceil(msLeft / 1000));
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  return `${m} min ${String(s % 60).padStart(2, "0")} s`;
}

export function latency(l: Latency): string {
  return typeof l === "string" ? l : `${l.min}–${l.max}`;
}

export function percent(p: number | undefined): string {
  const v = p ?? 1;
  return `${Math.round(v * 1000) / 10}%`;
}

/** O que o override faz, em uma linha: "sintetiza 503 · atrasa 2s · 30%". */
export function overrideSummary(o: Override): string {
  const parts: string[] = [];
  if (o.drop) parts.push("derruba a conexão");
  else if (o.respond) parts.push(`sintetiza ${o.respond.status ?? 200}`);
  if (o.latency) parts.push(`atrasa ${latency(o.latency)}`);
  if (parts.length === 0) parts.push("sem efeito declarado");
  parts.push(`em ${percent(o.probability)}`);
  return parts.join(" · ");
}

export function overrideSelector(o: Override): string {
  const m = o.match;
  const path = m.pathRegex ? `~${m.pathRegex}` : (m.path ?? "*");
  return (m.method ? m.method + " " : "") + path;
}

export function liveSummary(s: OverrideLiveState | undefined, enabled: boolean, now: number, at: number): string {
  if (!enabled) return "desligado";
  if (!s) return "ligado";
  if (s.expired === "ttl") return "expirou pelo TTL";
  if (s.expired === "applications") return "esgotou as aplicações";
  const parts = ["ativo"];
  if (s.ttlRemainingMs !== null) parts.push(`${seconds(s.ttlRemainingMs - (now - at))} restantes`);
  if (s.maxApplications !== null) parts.push(`${s.applications}/${s.maxApplications} aplicações`);
  else if (s.applications > 0) parts.push(`${s.applications} aplicações`);
  return parts.join(" · ");
}

export type InterventionTag = { label: string; tone: "synth" | "drop" | "delay" } | null;

export function interventionTag(e: Exchange): InterventionTag {
  const i = e.interventions ?? [];
  if (e.outcome === "dropped" || i.includes("dropped")) return { label: "queda", tone: "drop" };
  // Sintetizado e atrasado ao mesmo tempo: o atraso já aparece no waterfall.
  if (e.outcome === "synthesized" || i.includes("synthesized")) return { label: "sintetizado", tone: "synth" };
  if (i.includes("delayed")) return { label: "atrasado", tone: "delay" };
  return null;
}
