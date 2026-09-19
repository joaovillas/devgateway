// The effects a rule (override) declares — the synthesized response, the
// delay and the connection drop — and the frequency of each one. The gateway
// draws every declared effect independently on each request the rule selects:
// an effect that declares no frequency holds always, an effect that was not
// drawn is ignored as if it were not declared, and a request where none was
// drawn goes to the destination untouched (docs/api.md, "The override
// resource").
import type { Drop, Latency, LatencySpec, Override } from "./api";
import type { MergePatch } from "./patch";

/** The frequency of an effect that declares none: it holds on every selected call. */
export const ALWAYS = 1;

export type EffectKind = "respond" | "latency" | "drop";

/** The declared effects in the order the panel reads them. */
export const EFFECT_ORDER: EffectKind[] = ["respond", "latency", "drop"];

export interface Effect {
  kind: EffectKind;
  /** How often it holds, from 0 to 1. */
  chance: number;
  /**
   * The frequency is not the effect's own: it comes from the rule's legacy
   * `probability`, which the first adjustment migrates away.
   */
  inherited: boolean;
}

/** The latency in its long form, or null when the rule declares no delay. */
export function latencySpec(l: Latency | undefined): LatencySpec | null {
  if (l === undefined) return null;
  return typeof l === "string" ? { fixed: l } : l;
}

/** The drop in its long form, or null when the rule does not declare it. */
export function dropSpec(d: Drop | undefined): { chance?: number } | null {
  if (d === undefined || d === false) return null;
  return d === true ? {} : d;
}

export function dropDeclared(d: Drop | undefined): boolean {
  return dropSpec(d) !== null;
}

/** The rule declares this effect. */
export function declares(o: Override, kind: EffectKind): boolean {
  switch (kind) {
    case "respond":
      return o.respond !== undefined;
    case "latency":
      return latencySpec(o.latency) !== null;
    case "drop":
      return dropDeclared(o.drop);
  }
}

/** The effect's own frequency, or undefined when it does not declare one. */
function ownChance(o: Override, kind: EffectKind): number | undefined {
  switch (kind) {
    case "respond":
      return o.respond?.chance;
    case "latency":
      return latencySpec(o.latency)?.chance;
    case "drop":
      return dropSpec(o.drop)?.chance;
  }
}

/**
 * The frequency that actually applies to this effect: its own, or the rule's
 * legacy `probability` as the default of the effects without one, or always.
 * Null when the rule does not declare the effect at all.
 */
export function effectChance(o: Override, kind: EffectKind): number | null {
  if (!declares(o, kind)) return null;
  return ownChance(o, kind) ?? o.probability ?? ALWAYS;
}

/** Every effect the rule declares, with the frequency in force for each. */
export function declaredEffects(o: Override): Effect[] {
  const out: Effect[] = [];
  for (const kind of EFFECT_ORDER) {
    if (!declares(o, kind)) continue;
    const own = ownChance(o, kind);
    out.push({
      kind,
      chance: own ?? o.probability ?? ALWAYS,
      inherited: own === undefined && o.probability !== undefined,
    });
  }
  return out;
}

/**
 * The rule declares effects and every one of them is at 0%: nothing it says
 * can ever happen, so it is not intervening even though it is on and within
 * its TTL and application limit.
 */
export function neverApplies(o: Override): boolean {
  const effects = declaredEffects(o);
  return effects.length > 0 && effects.every((e) => e.chance <= 0);
}

/** The effect a summary shows for the rule: the first declared one that can still happen. */
export function leadingEffect(o: Override): Effect | null {
  const effects = declaredEffects(o);
  return effects.find((e) => e.chance > 0) ?? effects[0] ?? null;
}

/**
 * The latency written whole. PATCH of `latency` is a plain merge patch, so a
 * patch that named only the frequency would merge over the current value and
 * an object over the short form ("2s") would lose the duration. Every key of
 * the long form goes in the patch, with null on the ones this shape does not
 * use.
 */
function latencyValue(next: LatencySpec): MergePatch {
  return {
    fixed: next.fixed ?? null,
    min: next.min ?? null,
    max: next.max ?? null,
    chance: next.chance ?? null,
  };
}

/** Patch that writes the delay whole, or removes it with null. */
export function latencyPatch(next: LatencySpec | null): MergePatch {
  return { latency: next === null ? null : latencyValue(next) };
}

/** Patch that sets one effect's frequency, keeping everything else it declares. */
export function chancePatch(o: Override, kind: EffectKind, chance: number): MergePatch {
  switch (kind) {
    case "respond":
      return { respond: { chance } };
    case "latency":
      return latencyPatch({ ...(latencySpec(o.latency) ?? {}), chance });
    case "drop":
      return { drop: { chance } };
  }
}

/**
 * The patch that moves a legacy rule to per-effect frequencies: each effect
 * without its own keeps the frequency it had (the rule's `probability`), and
 * `probability` leaves the document. Empty when the rule is already migrated,
 * so it composes with any adjustment without adding a write.
 */
export function migrateProbability(o: Override): MergePatch {
  const p = o.probability;
  if (p === undefined) return {};
  const patch: MergePatch = { probability: null };
  // A legacy frequency of 1 is what an effect without one already means, so
  // dropping the field is the whole migration.
  if (p === ALWAYS) return patch;
  if (o.respond && o.respond.chance === undefined) patch.respond = { chance: p };
  const l = latencySpec(o.latency);
  if (l && l.chance === undefined) Object.assign(patch, latencyPatch({ ...l, chance: p }));
  const d = dropSpec(o.drop);
  if (d && d.chance === undefined) patch.drop = { chance: p };
  return patch;
}
