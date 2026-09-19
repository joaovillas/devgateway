// O encaminhamento em um exemplo: dada a entrada, o destino e as duas chaves
// do serviço, monta a requisição que o gateway faria ao destino, como o
// proxy a monta de verdade (internal/proxy/proxy.go, rewriteFor):
//
//   1. com "remove prefixo", o path perde a parte fixa do curinga
//      (PathPattern.Strip: só o prefixo de "/api/*"; num path exato não há o
//      que remover);
//   2. o path que sobrou é colado ao path do destino (httputil.SetURL, que
//      junta com uma barra só);
//   3. o Host enviado é o do destino quando "reescreve host" está ligado, e
//      o que o app mandou quando não está.
//
// É daqui que sai a prévia do painel: o que a tela mostra é o que o gateway
// faz, e não uma paráfrase dele.

/** A entrada do serviço já separada: parte fixa e curinga de sufixo. */
export interface EntryPattern {
  raw: string;
  /** Parte fixa do curinga: "/api" em "/api/*"; vazia num path exato. */
  prefix: string;
  wildcard: boolean;
}

export function parseEntry(path: string): EntryPattern {
  const raw = path.trim();
  if (raw.endsWith("/*")) return { raw, prefix: raw.slice(0, -2), wildcard: true };
  return { raw, prefix: "", wildcard: false };
}

/**
 * Um pedaço do path mostrado na prévia. `sample` marca o que foi inventado
 * para o exemplo (o trecho sob o curinga, o valor de um `:id`); o resto veio
 * do que o usuário digitou.
 */
export interface Piece {
  text: string;
  sample?: boolean;
}

export function pieceText(ps: Piece[]): string {
  return ps.map((p) => p.text).join("");
}

// Trechos de exemplo plausíveis: a entrada costuma dizer do que o serviço
// trata, e um exemplo reconhecível ensina mais que "algum/caminho". Sem
// pista, o genérico serve.
const CLUES: { test: RegExp; tail: string }[] = [
  { test: /cep/, tail: "40415345/json" },
  { test: /(usuari|user|cliente|conta|account)/, tail: "42" },
  { test: /(pedido|order|cobran|charge|pagament|payment)/, tail: "1234" },
  { test: /(produto|product|item|catalog|estoque|stock)/, tail: "p1" },
  { test: /(busca|search)/, tail: "termo" },
];

const GENERIC_TAIL = "algum/caminho";

/** O trecho de exemplo sob o curinga, escolhido pelo que a entrada e o destino dizem. */
function sampleTail(clue: string): string {
  const c = clue.toLowerCase();
  return CLUES.find((x) => x.test.test(c))?.tail ?? GENERIC_TAIL;
}

/** O valor de exemplo de um parâmetro de segmento (":id"): o primeiro trecho da pista. */
function sampleValue(clue: string): string {
  const first = sampleTail(clue).split("/")[0]!;
  return first === "algum" ? "123" : first;
}

/**
 * O path que o app chamaria, em pedaços: o literal que o usuário digitou e o
 * que foi inventado para o exemplo. Entrada vazia casa qualquer path, então
 * o exemplo é todo inventado.
 */
function requestPath(e: EntryPattern, clue: string): Piece[] {
  const tail = sampleTail(clue);
  if (!e.raw) return [{ text: "/" }, { text: tail, sample: true }];
  const base = e.wildcard ? e.prefix : e.raw;
  const out: Piece[] = [];
  let lit = "";
  base.split("/").forEach((seg, i) => {
    if (i > 0) lit += "/";
    if (i > 0 && seg.startsWith(":") && seg.length > 1) {
      out.push({ text: lit });
      out.push({ text: sampleValue(clue), sample: true });
      lit = "";
    } else {
      lit += seg;
    }
  });
  if (lit) out.push({ text: lit });
  if (e.wildcard) {
    out.push({ text: "/" });
    out.push({ text: tail, sample: true });
  }
  return out.filter((p) => p.text !== "");
}

/** PathPattern.Strip em pedaços: tira a parte fixa do curinga, mantendo ao menos "/". */
function strip(pieces: Piece[], prefix: string): Piece[] {
  let left = prefix.length;
  const out: Piece[] = [];
  for (const p of pieces) {
    if (left <= 0) {
      out.push(p);
      continue;
    }
    if (p.text.length <= left) {
      left -= p.text.length;
      continue;
    }
    out.push({ ...p, text: p.text.slice(left) });
    left = 0;
  }
  const joined = pieceText(out);
  if (!joined.startsWith("/")) out.unshift({ text: "/" });
  return out;
}

/**
 * singleJoiningSlash do httputil: o path do destino colado ao path que veio,
 * com uma barra só entre eles. O pedaço do destino sai separado para a prévia
 * poder mostrar de onde vem cada parte.
 */
function join(base: string, pieces: Piece[]): { base: string; rest: Piece[] } {
  const aslash = base.endsWith("/");
  const bslash = pieceText(pieces).startsWith("/");
  if (aslash && bslash) {
    const [first, ...others] = pieces;
    return { base, rest: first ? [{ ...first, text: first.text.slice(1) }, ...others] : [] };
  }
  if (!aslash && !bslash) return { base: base + "/", rest: pieces };
  return { base, rest: pieces };
}

/** O destino declarado, já separado; null quando não dá para ler a URL. */
export interface Destination {
  /** "viacep.com.br" ou "127.0.0.1:9001". */
  host: string;
  /** O path próprio do destino: "/ws"; vazio quando ele não tem um. */
  path: string;
  /** O destino está fora da máquina: nem localhost, nem IP privado. */
  external: boolean;
  https: boolean;
}

export function parseDestination(url: string): Destination | null {
  const raw = url.trim();
  if (!/^https?:\/\/.+/i.test(raw)) return null;
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return null;
  }
  if (!u.host) return null;
  const path = u.pathname === "/" ? "" : u.pathname.replace(/\/+$/, "");
  return { host: u.host, path, external: !isLocal(u.hostname), https: u.protocol === "https:" };
}

/** Endereços da própria máquina ou da rede local, onde o Host original costuma servir. */
function isLocal(hostname: string): boolean {
  const h = hostname.replace(/^\[|\]$/g, "").toLowerCase();
  if (h === "localhost" || h.endsWith(".localhost")) return true;
  if (h === "::1" || h === "0.0.0.0" || h === "::") return true;
  if (/^127\./.test(h)) return true;
  if (/^10\./.test(h)) return true;
  if (/^192\.168\./.test(h)) return true;
  if (/^172\.(1[6-9]|2\d|3[01])\./.test(h)) return true;
  if (/^169\.254\./.test(h)) return true;
  if (/^f[cd][0-9a-f]{2}:/.test(h)) return true;
  return h.endsWith(".local") || h.endsWith(".internal") || h.endsWith(".test");
}

export interface ForwardInput {
  /** `match.path` do serviço: "/viacep/*", ou vazio para qualquer entrada. */
  path: string;
  /** `match.host` do serviço, ou vazio para qualquer Host. */
  host: string;
  /** `upstream` do serviço: "http://viacep.com.br/ws". */
  destination: string;
  stripPrefix: boolean;
  rewriteHost: boolean;
  /** Porta de tráfego do processo; sem ela, a do exemplo. */
  trafficPort: number | undefined;
}

export interface Forwarding {
  /** O endereço que o app chama: "localhost:8080" ou o host da entrada. */
  appHost: string;
  appPath: Piece[];
  /** O que chega ao destino; null quando não há destino legível. */
  dest: {
    host: string;
    /** O path próprio do destino, já com a barra da junção. */
    base: string;
    /** O que o gateway anexou a ele. */
    rest: Piece[];
    /** O Host que o destino recebe. */
    hostHeader: string;
  } | null;
  /** O trecho inventado para o exemplo, para a linha que avisa disso. */
  sample: string | null;
  /** A entrada é um path exato: não existe prefixo para remover. */
  exact: boolean;
  entry: EntryPattern;
}

/** A porta de tráfego do exemplo enquanto o estado do processo não chegou. */
const EXAMPLE_PORT = 8080;

/** Monta a requisição de exemplo e o que o destino recebe dela. */
export function forwarding(inp: ForwardInput): Forwarding {
  const entry = parseEntry(inp.path);
  const dest = parseDestination(inp.destination);
  const clue = `${entry.raw} ${inp.destination}`;
  const appPath = requestPath(entry, clue);
  const port = inp.trafficPort ?? EXAMPLE_PORT;
  const h = inp.host.trim();
  const appHost = h ? (h.includes(":") ? h : `${h}:${port}`) : `localhost:${port}`;
  const sample = appPath.find((p) => p.sample)?.text ?? null;
  const exact = entry.raw !== "" && !entry.wildcard;

  if (!dest) return { appHost, appPath, dest: null, sample, exact, entry };
  const forwarded = inp.stripPrefix ? strip(appPath, entry.prefix) : appPath;
  const { base, rest } = join(dest.path, forwarded);
  return {
    appHost,
    appPath,
    dest: { host: dest.host, base, rest, hostHeader: inp.rewriteHost ? dest.host : appHost },
    sample,
    exact,
    entry,
  };
}

/** O que a prévia mostra na linha do destino, para dica e leitor de tela. */
export function destLine(f: Forwarding): string {
  return f.dest ? f.dest.host + f.dest.base + pieceText(f.dest.rest) : "";
}

// ---------- Padrões espertos do cadastro ----------

export interface Suggestion {
  stripPrefix: boolean;
  rewriteHost: boolean;
  /** Por que foi decidido assim, em uma linha. */
  why: string;
}

/**
 * O que sugerir para as duas chaves a partir do destino, enquanto o usuário
 * não as tocar. Destino de fora da máquina, ou com path próprio, quase nunca
 * quer o prefixo da entrada colado ao path dele nem o Host do app (é o que
 * faz um nginx responder 301 para o lugar errado): manda só o que vem depois
 * do prefixo, com o Host dele. Destino local e sem path é o app do dev atrás
 * do gateway: o path inteiro e o Host de entrada, que ele costuma usar para
 * montar as URLs que devolve.
 */
export function suggest(destination: string): Suggestion | null {
  const d = parseDestination(destination);
  if (!d) return null;
  if (d.external && d.path) {
    return {
      stripPrefix: true,
      rewriteHost: true,
      why: `destino externo com path próprio (${d.path}): só o que vem depois do prefixo, com o Host dele`,
    };
  }
  if (d.external) {
    return { stripPrefix: true, rewriteHost: true, why: "destino externo: enviando o Host dele e só o que vem depois do prefixo" };
  }
  if (d.path) {
    return {
      stripPrefix: true,
      rewriteHost: true,
      why: `o destino já tem o path ${d.path}: só o que vem depois do prefixo, para não repeti-lo`,
    };
  }
  return { stripPrefix: false, rewriteHost: false, why: "destino local sem path: o path inteiro e o Host que o seu app mandou" };
}
