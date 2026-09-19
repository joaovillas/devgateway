// JSON Merge Patch (RFC 7396), the format of the API PATCH bodies: the keys
// present replace the current ones, `null` removes the field and lists are
// swapped whole.

export type Json = null | boolean | number | string | Json[] | { [k: string]: Json };
export type MergePatch = { [k: string]: unknown };

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

export function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (Array.isArray(a) && Array.isArray(b)) {
    return a.length === b.length && a.every((x, i) => deepEqual(x, b[i]));
  }
  if (isObject(a) && isObject(b)) {
    const ka = Object.keys(a).filter((k) => a[k] !== undefined);
    const kb = Object.keys(b).filter((k) => b[k] !== undefined);
    return ka.length === kb.length && ka.every((k) => deepEqual(a[k], b[k]));
  }
  return false;
}

/** Applies a merge patch over `target` without changing it. */
export function applyMergePatch<T>(target: T, patch: MergePatch): T {
  const out: Record<string, unknown> = isObject(target) ? { ...target } : {};
  for (const [k, v] of Object.entries(patch)) {
    if (v === null) delete out[k];
    else if (isObject(v)) out[k] = applyMergePatch(out[k], v);
    else out[k] = v;
  }
  return out as T;
}

/** Joins two patches as if they were applied in sequence (`b` after `a`). */
export function composePatches(a: MergePatch, b: MergePatch): MergePatch {
  const out: MergePatch = { ...a };
  for (const [k, v] of Object.entries(b)) {
    const prev = out[k];
    out[k] = isObject(v) && isObject(prev) ? composePatches(prev, v) : v;
  }
  return out;
}

/**
 * `b` can be joined to `a` without changing the result. It cannot when `a`
 * removes a key (`null`) and `b` puts an object in it: joined they would turn
 * into a merge over the old value instead of the replacement by the new object.
 */
export function canCompose(a: MergePatch, b: MergePatch): boolean {
  for (const [k, v] of Object.entries(b)) {
    const prev = a[k];
    if (prev === null && isObject(v)) return false;
    if (isObject(prev) && isObject(v) && !canCompose(prev, v)) return false;
  }
  return true;
}

/**
 * The smallest patch that takes `from` to `to`. A key missing from `to`
 * becomes `null`, objects are compared key by key and the rest is swapped
 * whole.
 */
export function mergeDiff(from: unknown, to: unknown): MergePatch {
  const a = isObject(from) ? from : {};
  const b = isObject(to) ? to : {};
  const out: MergePatch = {};
  for (const k of Object.keys(a)) {
    if (a[k] !== undefined && b[k] === undefined) out[k] = null;
  }
  for (const [k, v] of Object.entries(b)) {
    if (v === undefined) continue;
    const prev = a[k];
    if (deepEqual(prev, v)) continue;
    if (isObject(v) && isObject(prev)) {
      const sub = mergeDiff(prev, v);
      if (Object.keys(sub).length > 0) out[k] = sub;
    } else {
      out[k] = v;
    }
  }
  return out;
}

export function isEmptyPatch(p: MergePatch): boolean {
  return Object.keys(p).length === 0;
}

/** Builds the nested patch for a dotted key: ("history.path", x) → {history:{path:x}}. */
export function patchForKey(key: string, value: unknown): MergePatch {
  const parts = key.split(".");
  let out: unknown = value;
  for (let i = parts.length - 1; i >= 0; i--) out = { [parts[i]!]: out };
  return out as MergePatch;
}
