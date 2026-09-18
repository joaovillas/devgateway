import { useEffect, useId, useRef, useState, type FocusEvent } from "react";
import type { Matcher } from "../api";
import { deepEqual } from "../patch";
import { CloseIcon, PlusIcon } from "./Icons";

// Editores de mapa: cabeçalhos e query dos critérios (valor é um Matcher) e
// cabeçalhos da resposta (valor é texto). Cada linha grava quando o foco sai
// dela, e remover a linha grava na hora.

export type MatchOp = "texto" | "equals" | "contains" | "regex" | "json";

export const MATCH_OPS: { value: MatchOp; label: string }[] = [
  { value: "texto", label: "igual a" },
  { value: "contains", label: "contém" },
  { value: "regex", label: "regex" },
  { value: "json", label: "json" },
  { value: "equals", label: "equals" },
];

export function matcherToRow(m: Matcher): { op: MatchOp; text: string } {
  if (typeof m === "string") return { op: "texto", text: m };
  if (m.regex !== undefined) return { op: "regex", text: m.regex };
  if (m.contains !== undefined) return { op: "contains", text: m.contains };
  if (m.equals !== undefined) return { op: "equals", text: m.equals };
  if (m.json !== undefined) return { op: "json", text: JSON.stringify(m.json) };
  return { op: "texto", text: "" };
}

/** Devolve o Matcher ou uma mensagem quando o texto não serve para o operador. */
export function rowToMatcher(op: MatchOp, text: string): Matcher | string[] {
  switch (op) {
    case "texto":
      return text;
    case "equals":
      return { equals: text };
    case "contains":
      return { contains: text };
    case "regex":
      try {
        new RegExp(text);
      } catch {
        return ["regex inválida"];
      }
      return { regex: text };
    case "json":
      try {
        return { json: JSON.parse(text) as unknown };
      } catch {
        return ["JSON inválido"];
      }
  }
}

interface RowState {
  id: number;
  key: string;
  op: MatchOp;
  text: string;
}

let nextRowId = 1;

function toRows(entries: Record<string, Matcher> | undefined): RowState[] {
  return Object.entries(entries ?? {}).map(([key, m]) => ({ id: nextRowId++, key, ...matcherToRow(m) }));
}

/**
 * Mapa nome → Matcher (critério de cabeçalho ou query) ou nome → texto
 * (`plain`, cabeçalhos da resposta). `onCommit` recebe o mapa inteiro.
 */
export function MapEditor({
  entries,
  onCommit,
  label,
  keyPlaceholder,
  plain = false,
}: {
  entries: Record<string, Matcher> | undefined;
  onCommit: (next: Record<string, Matcher> | undefined) => void;
  label: string;
  keyPlaceholder: string;
  plain?: boolean;
}) {
  const [rows, setRows] = useState<RowState[]>(() => toRows(entries));
  const [problem, setProblem] = useState<string | null>(null);
  const box = useRef<HTMLDivElement>(null);
  const errId = useId();

  useEffect(() => {
    if (box.current?.contains(document.activeElement)) return;
    setRows(toRows(entries));
    setProblem(null);
  }, [entries]);

  const build = (list: RowState[]): Record<string, Matcher> | string => {
    const out: Record<string, Matcher> = {};
    for (const r of list) {
      const k = r.key.trim();
      if (!k) continue;
      if (k in out) return `nome repetido: ${k}`;
      const m = plain ? r.text : rowToMatcher(r.op, r.text);
      if (Array.isArray(m)) return `${k}: ${m[0]}`;
      out[k] = m;
    }
    return out;
  };

  const commit = (list: RowState[]) => {
    const built = build(list);
    if (typeof built === "string") {
      setProblem(built);
      return;
    }
    setProblem(null);
    const next = Object.keys(built).length ? built : undefined;
    if (!deepEqual(next ?? {}, entries ?? {})) onCommit(next);
  };

  const onRowBlur = (e: FocusEvent<HTMLDivElement>) => {
    if (e.currentTarget.contains(e.relatedTarget as Node | null)) return;
    commit(rows);
  };

  const update = (id: number, patch: Partial<RowState>) =>
    setRows((rs) => rs.map((r) => (r.id === id ? { ...r, ...patch } : r)));

  return (
    <div className="kv" ref={box} role="group" aria-label={label} aria-describedby={problem ? errId : undefined}>
      {rows.map((r) => (
        <div className="kv__row" key={r.id} onBlur={onRowBlur}>
          <input
            className="input input--mono input--sm"
            value={r.key}
            placeholder={keyPlaceholder}
            aria-label={`${label}: nome`}
            spellCheck={false}
            onChange={(e) => update(r.id, { key: e.target.value })}
            onKeyDown={(e) => e.key === "Enter" && commit(rows)}
          />
          {plain ? null : (
            <select
              className="input input--sm select"
              value={r.op}
              aria-label={`${label}: operador de ${r.key || "novo item"}`}
              onChange={(e) => {
                const op = e.target.value as MatchOp;
                const list = rows.map((x) => (x.id === r.id ? { ...x, op } : x));
                setRows(list);
                commit(list);
              }}
            >
              {MATCH_OPS.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </select>
          )}
          <input
            className="input input--mono input--sm"
            value={r.text}
            aria-label={`${label}: valor de ${r.key || "novo item"}`}
            spellCheck={false}
            onChange={(e) => update(r.id, { text: e.target.value })}
            onKeyDown={(e) => e.key === "Enter" && commit(rows)}
          />
          <button
            type="button"
            className="icon-button"
            aria-label={`Remover ${r.key || "item vazio"} de ${label}`}
            onClick={() => {
              const list = rows.filter((x) => x.id !== r.id);
              setRows(list);
              commit(list);
            }}
          >
            <CloseIcon />
          </button>
        </div>
      ))}
      <button
        type="button"
        className="text-button"
        onClick={() => setRows((rs) => [...rs, { id: nextRowId++, key: "", op: "texto", text: "" }])}
      >
        <PlusIcon /> {plain ? "cabeçalho" : "critério"}
      </button>
      {problem ? (
        <span className="field__problem" id={errId} role="alert">
          {problem}
        </span>
      ) : null}
    </div>
  );
}

/** Critério do corpo: um Matcher só, ou nenhum. */
export function BodyMatcherEditor({
  value,
  onCommit,
  label,
}: {
  value: Matcher | undefined;
  onCommit: (next: Matcher | null) => void;
  label: string;
}) {
  const initial = value === undefined ? { op: "nenhum" as const, text: "" } : matcherToRow(value);
  const [op, setOp] = useState<MatchOp | "nenhum">(initial.op);
  const [text, setText] = useState(initial.text);
  const [problem, setProblem] = useState<string | null>(null);
  const box = useRef<HTMLSpanElement>(null);

  useEffect(() => {
    if (box.current?.contains(document.activeElement)) return;
    const r = value === undefined ? { op: "nenhum" as const, text: "" } : matcherToRow(value);
    setOp(r.op);
    setText(r.text);
    setProblem(null);
  }, [value]);

  const commit = (o: MatchOp | "nenhum", t: string) => {
    if (o === "nenhum") {
      setProblem(null);
      if (value !== undefined) onCommit(null);
      return;
    }
    const m = rowToMatcher(o, t);
    if (Array.isArray(m)) {
      setProblem(m[0]!);
      return;
    }
    setProblem(null);
    if (!deepEqual(m, value)) onCommit(m);
  };

  return (
    <span className="kv__row kv__row--single" ref={box}>
      <select
        className="input input--sm select"
        value={op}
        aria-label={`${label}: operador`}
        onChange={(e) => {
          const o = e.target.value as MatchOp | "nenhum";
          setOp(o);
          commit(o, text);
        }}
      >
        <option value="nenhum">qualquer corpo</option>
        {MATCH_OPS.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
      {op === "nenhum" ? null : (
        <input
          className={"input input--mono input--sm" + (problem ? " input--bad" : "")}
          value={text}
          aria-label={`${label}: valor`}
          aria-invalid={problem ? true : undefined}
          spellCheck={false}
          onChange={(e) => setText(e.target.value)}
          onBlur={() => commit(op, text)}
          onKeyDown={(e) => e.key === "Enter" && commit(op, text)}
        />
      )}
      {problem ? (
        <span className="field__problem" role="alert">
          {problem}
        </span>
      ) : null}
    </span>
  );
}
