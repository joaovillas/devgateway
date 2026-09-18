// JSON Merge Patch (RFC 7396), o formato dos PATCH da API: as chaves presentes
// substituem as atuais, `null` remove o campo e listas são trocadas inteiras.

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

/** Aplica um merge patch sobre `target` sem alterá-lo. */
export function applyMergePatch<T>(target: T, patch: MergePatch): T {
  const out: Record<string, unknown> = isObject(target) ? { ...target } : {};
  for (const [k, v] of Object.entries(patch)) {
    if (v === null) delete out[k];
    else if (isObject(v)) out[k] = applyMergePatch(out[k], v);
    else out[k] = v;
  }
  return out as T;
}

/** Junta dois patches como se fossem aplicados em sequência (`b` depois de `a`). */
export function composePatches(a: MergePatch, b: MergePatch): MergePatch {
  const out: MergePatch = { ...a };
  for (const [k, v] of Object.entries(b)) {
    const prev = out[k];
    out[k] = isObject(v) && isObject(prev) ? composePatches(prev, v) : v;
  }
  return out;
}

/**
 * `b` pode ser juntado a `a` sem mudar o resultado. Não pode quando `a` remove
 * uma chave (`null`) e `b` põe um objeto nela: juntos virariam um merge sobre
 * o valor antigo, e não a troca pelo objeto novo.
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
 * O menor patch que leva `from` a `to`. Chave ausente em `to` vira `null`,
 * objetos são comparados chave a chave e o resto é trocado inteiro.
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

/** Monta o patch aninhado de uma chave pontuada: ("history.path", x) → {history:{path:x}}. */
export function patchForKey(key: string, value: unknown): MergePatch {
  const parts = key.split(".");
  let out: unknown = value;
  for (let i = parts.length - 1; i >= 0; i--) out = { [parts[i]!]: out };
  return out as MergePatch;
}
