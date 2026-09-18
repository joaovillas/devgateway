// Realce do documento só com os níveis de tinta, sem matiz: a cor continua
// reservada para estado. Chave em --ink-2, valor em --ink, pontuação e
// comentário em --ink-3. O realce é uma camada por baixo do textarea: o texto
// de cada linha é reproduzido caractere por caractere, só dividido em trechos.

export type TokenKind = "key" | "value" | "punct" | "comment";
export interface Token {
  text: string;
  kind: TokenKind;
}

/** Divide uma linha em trechos cuja concatenação é exatamente a linha. */
export function highlightLine(line: string, lang: "yaml" | "json"): Token[] {
  return lang === "yaml" ? yamlLine(line) : jsonLine(line);
}

const YAML_KEY =
  /^(\s*)((?:-\s+)*)("(?:[^"\\]|\\.)*"|'[^']*'|[^\s#'"\-:][^:#]*?|-[^\s:#][^:#]*?)(\s*:)(?=\s|$)(.*)$/;

function yamlLine(line: string): Token[] {
  const out: Token[] = [];
  const push = (text: string, kind: TokenKind) => {
    if (text) out.push({ text, kind });
  };
  const comment = /^(\s*)(#.*)$/.exec(line);
  if (comment) {
    push(comment[1]!, "value");
    push(comment[2]!, "comment");
    return out;
  }
  const m = YAML_KEY.exec(line);
  if (m) {
    push(m[1]!, "value");
    push(m[2]!, "punct");
    push(m[3]!, "key");
    push(m[4]!, "punct");
    yamlValue(m[5]!, push);
    return out;
  }
  const item = /^(\s*)((?:-(?:\s+|$))*)(.*)$/.exec(line)!;
  push(item[1]!, "value");
  push(item[2]!, "punct");
  yamlValue(item[3]!, push);
  return out;
}

/** Valor com um possível comentário no fim (um # precedido de espaço, fora de aspas). */
function yamlValue(s: string, push: (text: string, kind: TokenKind) => void) {
  let quote: string | null = null;
  for (let i = 0; i < s.length; i++) {
    const c = s[i]!;
    if (quote) {
      if (c === "\\" && quote === '"') i++;
      else if (c === quote) quote = null;
    } else if (c === '"' || c === "'") {
      quote = c;
    } else if (c === "#" && (i === 0 || /\s/.test(s[i - 1]!))) {
      push(s.slice(0, i), "value");
      push(s.slice(i), "comment");
      return;
    }
  }
  push(s, "value");
}

const JSON_TOKEN = /("(?:[^"\\]|\\.)*")(\s*:)?|([{}[\],:])/g;

function jsonLine(line: string): Token[] {
  const out: Token[] = [];
  let last = 0;
  for (const m of line.matchAll(JSON_TOKEN)) {
    const at = m.index;
    if (at > last) out.push({ text: line.slice(last, at), kind: "value" });
    if (m[1] !== undefined) {
      if (m[2] !== undefined) {
        out.push({ text: m[1], kind: "key" });
        out.push({ text: m[2], kind: "punct" });
      } else {
        out.push({ text: m[1], kind: "value" });
      }
    } else {
      out.push({ text: m[3]!, kind: "punct" });
    }
    last = at + m[0].length;
  }
  if (last < line.length) out.push({ text: line.slice(last), kind: "value" });
  return out;
}
