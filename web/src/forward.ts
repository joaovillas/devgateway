// The forwarding shown as an example: given the entry, the destination and
// the service's two keys, this builds the request the gateway would make to
// the destination, the way the proxy really builds it (internal/proxy/proxy.go,
// rewriteFor):
//
//   1. with "strip prefix", the path loses the fixed part of the wildcard
//      (PathPattern.Strip: only the prefix of "/api/*"; on an exact path there
//      is nothing to strip);
//   2. what is left of the path is glued to the destination's path
//      (httputil.SetURL, which joins them with a single slash);
//   3. the Host that is sent is the destination's when "rewrite host" is on,
//      and the one the app sent when it is not.
//
// This is where the panel's preview comes from: what the screen shows is what
// the gateway does, and not a paraphrase of it.

/** The service's entry already taken apart: fixed part and suffix wildcard. */
export interface EntryPattern {
  raw: string;
  /** The fixed part of the wildcard: "/api" in "/api/*"; empty on an exact path. */
  prefix: string;
  wildcard: boolean;
}

export function parseEntry(path: string): EntryPattern {
  const raw = path.trim();
  if (raw.endsWith("/*")) return { raw, prefix: raw.slice(0, -2), wildcard: true };
  return { raw, prefix: "", wildcard: false };
}

/**
 * A piece of the path shown in the preview. `sample` marks what was invented
 * for the example (the stretch under the wildcard, the value of an `:id`); the
 * rest came from what the user typed.
 */
export interface Piece {
  text: string;
  sample?: boolean;
}

export function pieceText(ps: Piece[]): string {
  return ps.map((p) => p.text).join("");
}

// Plausible example stretches: the entry usually says what the service deals
// with, and a recognizable example teaches more than "some/path". With no
// clue, the generic one does the job.
const CLUES: { test: RegExp; tail: string }[] = [
  { test: /(zip|postcode|cep)/, tail: "40415345/json" },
  { test: /(usuari|user|cliente|conta|account)/, tail: "42" },
  { test: /(pedido|order|cobran|charge|pagament|payment)/, tail: "1234" },
  { test: /(produto|product|item|catalog|estoque|stock)/, tail: "p1" },
  { test: /(busca|search)/, tail: "term" },
];

const GENERIC_TAIL = "some/path";

/** The example stretch under the wildcard, chosen by what the entry and the destination say. */
function sampleTail(clue: string): string {
  const c = clue.toLowerCase();
  return CLUES.find((x) => x.test.test(c))?.tail ?? GENERIC_TAIL;
}

/** The example value of a segment parameter (":id"): the first stretch of the clue. */
function sampleValue(clue: string): string {
  const first = sampleTail(clue).split("/")[0]!;
  return first === "some" ? "123" : first;
}

/**
 * The path the app would call, in pieces: the literal the user typed and what
 * was invented for the example. An empty entry matches any path, so the whole
 * example is invented.
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

/** PathPattern.Strip in pieces: takes off the fixed part of the wildcard, keeping at least "/". */
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
 * httputil's singleJoiningSlash: the destination's path glued to the path that
 * came in, with a single slash between them. The destination's piece comes out
 * separate so the preview can show where each part comes from.
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

/** The declared destination, already taken apart; null when the URL cannot be read. */
export interface Destination {
  /** "zipapi.example.com" or "127.0.0.1:9001". */
  host: string;
  /** The destination's own path: "/ws"; empty when it has none. */
  path: string;
  /** The destination is off this machine: neither localhost nor a private IP. */
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

/** Addresses on this machine or on the local network, where the original Host usually serves. */
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
  /** The service's `match.path`: "/zip/*", or empty for any entry. */
  path: string;
  /** The service's `match.host`, or empty for any Host. */
  host: string;
  /** The service's `upstream`: "http://zipapi.example.com/ws". */
  destination: string;
  stripPrefix: boolean;
  rewriteHost: boolean;
  /** The process's traffic port; without it, the example one. */
  trafficPort: number | undefined;
}

export interface Forwarding {
  /** The address the app calls: "localhost:8080" or the entry's host. */
  appHost: string;
  appPath: Piece[];
  /** What reaches the destination; null when there is no readable destination. */
  dest: {
    host: string;
    /** The destination's own path, already with the joining slash. */
    base: string;
    /** What the gateway appended to it. */
    rest: Piece[];
    /** The Host the destination receives. */
    hostHeader: string;
  } | null;
  /** The stretch invented for the example, for the line that says so. */
  sample: string | null;
  /** The entry is an exact path: there is no prefix to strip. */
  exact: boolean;
  entry: EntryPattern;
}

/** The example's traffic port while the process state has not arrived. */
const EXAMPLE_PORT = 8080;

/** Builds the example request and what the destination receives of it. */
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

/** What the preview shows on the destination line, for the tooltip and the screen reader. */
export function destLine(f: Forwarding): string {
  return f.dest ? f.dest.host + f.dest.base + pieceText(f.dest.rest) : "";
}

// ---------- Smart defaults for the new-service form ----------

export interface Suggestion {
  stripPrefix: boolean;
  rewriteHost: boolean;
  /** Why it was decided that way, in one line. */
  why: string;
}

/**
 * What to suggest for the two keys from the destination alone, for as long as
 * the user does not touch them. A destination off this machine, or one with a
 * path of its own, almost never wants the entry's prefix glued to its path nor
 * the app's Host (that is what makes an nginx answer 301 to the wrong place):
 * send only what comes after the prefix, with its own Host. A local
 * destination with no path is the dev's app behind the gateway: the whole path
 * and the incoming Host, which it usually uses to build the URLs it returns.
 */
export function suggest(destination: string): Suggestion | null {
  const d = parseDestination(destination);
  if (!d) return null;
  if (d.external && d.path) {
    return {
      stripPrefix: true,
      rewriteHost: true,
      why: `external destination with a path of its own (${d.path}): only what comes after the prefix, with its Host`,
    };
  }
  if (d.external) {
    return { stripPrefix: true, rewriteHost: true, why: "external destination: sending its Host and only what comes after the prefix" };
  }
  if (d.path) {
    return {
      stripPrefix: true,
      rewriteHost: true,
      why: `the destination already has the path ${d.path}: only what comes after the prefix, so as not to repeat it`,
    };
  }
  return { stripPrefix: false, rewriteHost: false, why: "local destination with no path: the whole path and the Host your app sent" };
}
