import type { Exchange, Latency } from "./api";

// Rule for numbers: what is read on screen follows en-US (decimal point,
// thousands comma: "2.08 s", "12.5%", "39.1 KiB"). What is typed, or copied
// back into the configuration, follows the format Go and YAML accept
// ("1.5s", "150ms"), because that is what the server takes: formatDuration
// and the duration fields do not go through here.

/** Number with a decimal point and a thousands separator. */
export function num(v: number, digits = 0): string {
  return v.toLocaleString("en-US", { minimumFractionDigits: digits, maximumFractionDigits: digits });
}

export function clock(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const p = (n: number, w = 2) => String(n).padStart(w, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(), 3)}`;
}

export function ms(v: number): string {
  // Zero is exact: "0 ms", without the decimal that would suggest a rounded measurement.
  if (v === 0) return "0 ms";
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

/** The delay in words: a fixed duration ("2s") or the range drawn on each call. */
export function latency(l: Latency): string {
  if (typeof l === "string") return l;
  if (l.min !== undefined || l.max !== undefined) return `${l.min ?? "0s"}–${l.max ?? l.min ?? "0s"}`;
  return l.fixed ?? "0s";
}

export function percent(p: number | undefined): string {
  const v = p ?? 1;
  const pct = Math.round(v * 1000) / 10;
  return `${num(pct, Number.isInteger(pct) ? 0 : 1)}%`;
}

export type InterventionTag = { label: string; tone: "synth" | "drop" | "delay" } | null;

export function interventionTag(e: Exchange): InterventionTag {
  const i = e.interventions ?? [];
  if (e.outcome === "dropped" || i.includes("dropped")) return { label: "dropped", tone: "drop" };
  // Synthesized and delayed at once: the delay already shows in the waterfall.
  if (e.outcome === "synthesized" || i.includes("synthesized")) return { label: "synthesized", tone: "synth" };
  if (i.includes("delayed")) return { label: "delayed", tone: "delay" };
  return null;
}

// ---------- Durations in Go's format ----------

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

/** "1m30s" → 90000. Returns null when the text is not a Go duration. */
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

/** 90000 → "1m30s", 150 → "150ms", 1500 → "1.5s": text Go takes back. */
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

/** Rounds to two significant digits: 1234 → 1200. */
export function nice(v: number): number {
  if (v <= 0) return 0;
  const p = 10 ** (Math.floor(Math.log10(v)) - 1);
  return Math.round(v / p) * p;
}

/**
 * Size in bytes. `scale` picks the unit: in a sentence with two sizes ("kept
 * X of Y"), both use the scale of the larger one, so that KiB is not mixed
 * with B. An exact value shows as an integer; the rest, one decimal.
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
 * Short file path for the screen: the file and its folder
 * ("routes/payments.yaml"). The API returns the path as the process resolved
 * it, which may be absolute; the whole path goes in the tooltip.
 */
export function shortPath(path: string): string {
  const parts = path.split(/[\\/]+/).filter(Boolean);
  return parts.length <= 2 ? path : parts.slice(-2).join("/");
}

/** A fragment of a rule's path: a literal, or a segment parameter (":id"). */
export interface PathPart {
  text: string;
  param: boolean;
}

/**
 * Splits a rule's path into fragments, separating the segment parameters
 * (":id" in "/zip/:id/json"), which match any non-empty value in that
 * segment. A ":" in the middle of a segment is a literal.
 */
export function pathParts(path: string): PathPart[] {
  const out: PathPart[] = [];
  let lit = "";
  path.split("/").forEach((seg, i) => {
    if (i > 0) lit += "/";
    if (i > 0 && seg.startsWith(":")) {
      if (lit) out.push({ text: lit, param: false });
      out.push({ text: seg, param: true });
      lit = "";
    } else {
      lit += seg;
    }
  });
  if (lit) out.push({ text: lit, param: false });
  return out;
}

const PARAM_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;

/**
 * Checks a rule's path the way the gateway checks it, so the problem shows
 * next to the field before anything is written. Returns null when the path is
 * accepted (including the empty one, which means "any").
 */
export function rulePathProblem(path: string): string | null {
  const p = path.trim();
  if (!p) return null;
  if (!p.startsWith("/")) return "the path starts with /";
  const seen = new Set<string>();
  for (const seg of p.slice(1).split("/")) {
    if (!seg.startsWith(":")) continue;
    const name = seg.slice(1);
    if (name.includes("*")) return "a segment cannot be a parameter and a wildcard at once";
    if (!PARAM_NAME.test(name)) return `invalid parameter ${seg}: the name starts with a letter or _ and continues with letters, digits or _, as in :id`;
    if (seen.has(name)) return `the parameter :${name} appears twice`;
    seen.add(name);
  }
  const star = p.indexOf("*");
  if (star >= 0 && (star !== p.length - 1 || !p.endsWith("/*"))) return "the wildcard only holds at the end, after /, as in /api/*";
  return null;
}

/** Short hint for a rule's path field. */
export const PATH_HINT = "use :id for a variable segment; wildcard at the end: /api/x/*";

/**
 * The message of an API validation error without the file and field prefix
 * ("routes/x.yaml: field overrides[1].match.path: ..."), to show it next to
 * the field. Returns null when the error is not about the field ending in
 * `suffix`.
 */
export function fieldProblem(body: { field?: string; message: string }, suffix: string): string | null {
  if (!body.field?.endsWith(suffix)) return null;
  const m = /field \S+(?: \([^)]*\))?: ([\s\S]*)$/.exec(body.message);
  return m?.[1] ?? body.message;
}
