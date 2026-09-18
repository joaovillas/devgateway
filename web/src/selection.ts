import { useCallback, useEffect, useState } from "react";

/**
 * O que está selecionado no mapa. Uma rota abre os controles e o documento
 * dela; um upstream só filtra o tráfego e destaca as rotas que apontam para ele.
 */
export type Selection = { kind: "route"; name: string } | { kind: "upstream"; name: string } | null;

// A seleção vive no fragmento (#rota=payments, #upstream=http%3A%2F%2F...) para
// sobreviver a um F5 e poder ser colada num link.
function parse(hash: string): Selection {
  const m = /^#(rota|upstream)=(.+)$/.exec(hash);
  if (!m?.[2]) return null;
  let name: string;
  try {
    name = decodeURIComponent(m[2]);
  } catch {
    return null;
  }
  return m[1] === "rota" ? { kind: "route", name } : { kind: "upstream", name };
}

function format(s: Selection): string {
  if (!s) return "";
  return (s.kind === "route" ? "#rota=" : "#upstream=") + encodeURIComponent(s.name);
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

  // Fragmento editado à mão ou navegação pelo histórico do navegador.
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
