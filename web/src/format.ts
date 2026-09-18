import type { Exchange, Latency, Override, OverrideLiveState } from "./api";

// Regra dos números: o que é lido na tela segue o pt-BR (vírgula decimal,
// ponto de milhar: "2,08 s", "12,5%", "39,1 KiB"). O que é digitado ou
// copiado de volta para a configuração segue o formato do Go e do YAML
// ("1.5s", "150ms"), porque é isso que o servidor aceita: formatDuration e os
// campos de duração não passam por aqui.

/** Número com vírgula decimal e ponto de milhar. */
export function num(v: number, digits = 0): string {
  return v.toLocaleString("pt-BR", { minimumFractionDigits: digits, maximumFractionDigits: digits });
}

export function clock(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const p = (n: number, w = 2) => String(n).padStart(w, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(), 3)}`;
}

export function ms(v: number): string {
  if (v >= 10_000) return num(v / 1000, 1) + " s";
  if (v >= 1000) return num(v / 1000, 2) + " s";
  if (v >= 10) return num(Math.round(v)) + " ms";
  return num(v, 1) + " ms";
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
  const pct = Math.round(v * 1000) / 10;
  return `${num(pct, Number.isInteger(pct) ? 0 : 1)}%`;
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

// ---------- Durações no formato do Go ----------

const UNIT_MS: Record<string, number> = {
  ns: 1e-6,
  us: 1e-3,
  "µs": 1e-3,
  "μs": 1e-3,
  ms: 1,
  s: 1000,
  m: 60_000,
  h: 3_600_000,
};

/** "1m30s" → 90000. Devolve null quando o texto não é uma duração do Go. */
export function parseDuration(text: string): number | null {
  const s = text.trim();
  if (s === "0") return 0;
  const re = /(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/gy;
  let total = 0;
  let pos = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(s))) {
    total += Number(m[1]) * UNIT_MS[m[2]!]!;
    pos = re.lastIndex;
  }
  if (pos === 0 || pos !== s.length) return null;
  return total;
}

/** 90000 → "1m30s", 150 → "150ms", 1500 → "1.5s": texto que o Go aceita de volta. */
export function formatDuration(v: number): string {
  if (v <= 0) return "0s";
  if (v < 1) return `${Math.round(v * 1000)}us`;
  if (v < 1000) return `${Math.round(v)}ms`;
  if (v < 60_000) return `${Math.round(v) / 1000}s`;
  const h = Math.floor(v / 3_600_000);
  const m = Math.floor((v % 3_600_000) / 60_000);
  const s = Math.round((v % 60_000) / 10) / 100;
  return (h ? `${h}h` : "") + (m || h ? `${m}m` : "") + (s ? `${s}s` : "");
}

/** Arredonda para dois algarismos significativos: 1234 → 1200. */
export function nice(v: number): number {
  if (v <= 0) return 0;
  const p = 10 ** (Math.floor(Math.log10(v)) - 1);
  return Math.round(v / p) * p;
}

/**
 * Tamanho em bytes. `scale` escolhe a unidade: numa frase com dois tamanhos
 * ("guardados X de Y"), os dois usam a escala do maior, para não misturar
 * KiB com B. Exato mostra inteiro; o resto, uma casa.
 */
export function bytes(n: number, scale = n): string {
  const unit = (div: number, name: string) => {
    const v = n / div;
    return `${num(v, Number.isInteger(v) ? 0 : 1)} ${name}`;
  };
  if (scale >= 1 << 20) return unit(1 << 20, "MiB");
  if (scale >= 1024) return unit(1024, "KiB");
  return `${num(n)} B`;
}

/**
 * Caminho de arquivo curto para a tela: o arquivo e a pasta dele
 * ("routes/payments.yaml"). A API devolve o caminho como o processo o
 * resolveu, que pode ser absoluto; o caminho inteiro vai na dica.
 */
export function shortPath(path: string): string {
  const parts = path.split(/[\\/]+/).filter(Boolean);
  return parts.length <= 2 ? path : parts.slice(-2).join("/");
}
