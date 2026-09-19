import { useCallback, useEffect, useState } from "react";

/**
 * What is selected in the services list. A service (route) opens its own
 * controls; a destination (upstream) only filters the traffic and highlights
 * the services that point at it.
 */
export type Selection = { kind: "route"; name: string } | { kind: "upstream"; name: string } | null;

// The selection lives in the fragment (#route=payments, #upstream=http%3A%2F%2F...)
// so that it survives an F5 and can be pasted into a link.
function parse(hash: string): Selection {
  const m = /^#(route|upstream)=(.+)$/.exec(hash);
  if (!m?.[2]) return null;
  let name: string;
  try {
    name = decodeURIComponent(m[2]);
  } catch {
    return null;
  }
  return m[1] === "route" ? { kind: "route", name } : { kind: "upstream", name };
}

function format(s: Selection): string {
  if (!s) return "";
  return (s.kind === "route" ? "#route=" : "#upstream=") + encodeURIComponent(s.name);
}

export function sameSelection(a: Selection, b: Selection): boolean {
  return a?.kind === b?.kind && a?.name === b?.name;
}

export function useSelection(): [Selection, (s: Selection) => void] {
  const [sel, setSel] = useState<Selection>(() => parse(window.location.hash));

  useEffect(() => {
    const hash = format(sel);
    if (window.location.hash !== hash) {
      history.replaceState(null, "", window.location.pathname + window.location.search + hash);
    }
  }, [sel]);

  // Fragment edited by hand, or navigation through the browser history.
  useEffect(() => {
    const onHash = () => setSel((cur) => {
      const next = parse(window.location.hash);
      return sameSelection(cur, next) ? cur : next;
    });
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  const set = useCallback((s: Selection) => setSel(s), []);
  return [sel, set];
}
