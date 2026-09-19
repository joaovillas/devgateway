// The two detail modes of the right-hand panel. Simple is the default: per
// rule, only the on/off switch, the name, the summarized effect and the
// frequency of each effect. Advanced reveals the latency, the drop, the
// criteria, the response, the lifetime and the application limit, plus the
// fields of the service and of the process.
import type { Override } from "./api";
import { dropDeclared, latencySpec } from "./effects";

export type DetailMode = "simple" | "advanced";

const MODE_KEY = "gateway.panel.mode";

export const MODE_OPTIONS: { value: DetailMode; label: string }[] = [
  { value: "simple", label: "simple" },
  { value: "advanced", label: "advanced" },
];

/** The mode remembered between visits; with nothing stored, the simple one. */
export function readMode(): DetailMode {
  try {
    return window.localStorage.getItem(MODE_KEY) === "advanced" ? "advanced" : "simple";
  } catch {
    return "simple";
  }
}

export function storeMode(m: DetailMode): void {
  try {
    window.localStorage.setItem(MODE_KEY, m);
  } catch {
    // Convenience only: without storage, the mode goes back to simple on F5.
  }
}

function filled(r: Record<string, unknown> | undefined): boolean {
  return r !== undefined && Object.keys(r).length > 0;
}

/**
 * What this rule uses that only the advanced mode edits, named for the
 * summary of the simple one. Empty means the simple mode shows the whole rule.
 */
export function advancedFeatures(o: Override): string[] {
  const out: string[] = [];
  if (latencySpec(o.latency)) out.push("latency");
  if (dropDeclared(o.drop)) out.push("a connection drop");
  if (o.ttl) out.push("a lifetime");
  if (o.maxApplications != null) out.push("an application limit");
  const m = o.match;
  if (m.pathRegex || filled(m.headers) || filled(m.query) || m.body !== undefined) {
    out.push("criteria beyond path and method");
  }
  return out;
}

/** "has latency and an application limit; open advanced to edit". */
export function advancedNote(features: string[]): string {
  const last = features[features.length - 1] ?? "";
  const list = features.length > 1 ? `${features.slice(0, -1).join(", ")} and ${last}` : last;
  return `has ${list}; open advanced to edit`;
}
