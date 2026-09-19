// Os dois modos de detalhe do painel da direita. O simples é o padrão: por
// regra, só o liga/desliga, o nome, o efeito resumido e a probabilidade. O
// avançado revela latência, queda, critérios, resposta, tempo de vida e
// limite de aplicações, além dos campos do serviço e do processo.
import type { Override } from "./api";

export type DetailMode = "simple" | "advanced";

const MODE_KEY = "gateway.painel.modo";

export const MODE_OPTIONS: { value: DetailMode; label: string }[] = [
  { value: "simple", label: "simples" },
  { value: "advanced", label: "avançado" },
];

/** O modo lembrado entre visitas; sem nada guardado, o simples. */
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
    // Só conveniência: sem armazenamento, o modo volta ao simples no F5.
  }
}

function filled(r: Record<string, unknown> | undefined): boolean {
  return r !== undefined && Object.keys(r).length > 0;
}

/**
 * O que esta regra usa e só o modo avançado edita, nomeado para o resumo do
 * simples. Vazio quer dizer que o simples mostra a regra inteira.
 */
export function advancedFeatures(o: Override): string[] {
  const out: string[] = [];
  if (o.latency) out.push("latência");
  if (o.drop) out.push("queda de conexão");
  if (o.ttl) out.push("tempo de vida");
  if (o.maxApplications != null) out.push("limite de aplicações");
  const m = o.match;
  if (m.pathRegex || filled(m.headers) || filled(m.query) || m.body !== undefined) {
    out.push("critérios além de path e método");
  }
  return out;
}

/** "tem latência e limite de aplicações; abra o avançado para editar". */
export function advancedNote(features: string[]): string {
  const last = features[features.length - 1] ?? "";
  const list = features.length > 1 ? `${features.slice(0, -1).join(", ")} e ${last}` : last;
  return `tem ${list}; abra o avançado para editar`;
}
