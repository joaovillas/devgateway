import { useEffect, useId, useRef, useState, type FocusEvent } from "react";
import type { Matcher } from "../api";
import { deepEqual } from "../patch";
import { CloseIcon, PlusIcon } from "./Icons";

// Map editors: header and query criteria (the value is a Matcher) and response
// headers (the value is text). Each row saves when focus leaves it, and
// removing the row saves right away.

export type MatchOp = "text" | "equals" | "contains" | "regex" | "json";

export const MATCH_OPS: { value: MatchOp; label: string }[] = [
  { value: "text", label: "is" },
  { value: "contains", label: "contains" },
  { value: "regex", label: "regex" },
  { value: "json", label: "json" },
  { value: "equals", label: "equals" },
];

export function matcherToRow(m: Matcher): { op: MatchOp; text: string } {
  if (typeof m === "string") return { op: "text", text: m };
  if (m.regex !== undefined) return { op: "regex", text: m.regex };
  if (m.contains !== undefined) return { op: "contains", text: m.contains };
  if (m.equals !== undefined) return { op: "equals", text: m.equals };
  if (m.json !== undefined) return { op: "json", text: JSON.stringify(m.json) };
  return { op: "text", text: "" };
}

/** Returns the Matcher, or a message when the text does not work for the operator. */
export function rowToMatcher(op: MatchOp, text: string): Matcher | string[] {
  switch (op) {
    case "text":
      return text;
    case "equals":
      return { equals: text };
    case "contains":
      return { contains: text };
    case "regex":
      try {
        new RegExp(text);
      } catch {
        return ["invalid regex"];
      }
      return { regex: text };
    case "json":
      try {
        return { json: JSON.parse(text) as unknown };
      } catch {
        return ["invalid JSON"];
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
 * Map of name → Matcher (header or query criterion) or name → text
 * (`plain`, response headers). `onCommit` receives the whole map.
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
      if (k in out) return `repeated name: ${k}`;
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
            aria-label={`${label}: name`}
            spellCheck={false}
            onChange={(e) => update(r.id, { key: e.target.value })}
            onKeyDown={(e) => e.key === "Enter" && commit(rows)}
          />
          {plain ? null : (
            <select
              className="input input--sm select"
              value={r.op}
              aria-label={`${label}: operator for ${r.key || "new item"}`}
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
            aria-label={`${label}: value for ${r.key || "new item"}`}
            spellCheck={false}
            onChange={(e) => update(r.id, { text: e.target.value })}
            onKeyDown={(e) => e.key === "Enter" && commit(rows)}
          />
          <button
            type="button"
            className="icon-button"
            aria-label={`Remove ${r.key || "empty item"} from ${label}`}
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
        onClick={() => setRows((rs) => [...rs, { id: nextRowId++, key: "", op: "text", text: "" }])}
      >
        <PlusIcon /> {plain ? "header" : "criterion"}
      </button>
      {problem ? (
        <span className="field__problem" id={errId} role="alert">
          {problem}
        </span>
      ) : null}
    </div>
  );
}

/** Body criterion: a single Matcher, or none. */
export function BodyMatcherEditor({
  value,
  onCommit,
  label,
}: {
  value: Matcher | undefined;
  onCommit: (next: Matcher | null) => void;
  label: string;
}) {
  const initial = value === undefined ? { op: "none" as const, text: "" } : matcherToRow(value);
  const [op, setOp] = useState<MatchOp | "none">(initial.op);
  const [text, setText] = useState(initial.text);
  const [problem, setProblem] = useState<string | null>(null);
  const box = useRef<HTMLSpanElement>(null);

  useEffect(() => {
    if (box.current?.contains(document.activeElement)) return;
    const r = value === undefined ? { op: "none" as const, text: "" } : matcherToRow(value);
    setOp(r.op);
    setText(r.text);
    setProblem(null);
  }, [value]);

  const commit = (o: MatchOp | "none", t: string) => {
    if (o === "none") {
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
        aria-label={`${label}: operator`}
        onChange={(e) => {
          const o = e.target.value as MatchOp | "none";
          setOp(o);
          commit(o, text);
        }}
      >
        <option value="none">any body</option>
        {MATCH_OPS.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
      {op === "none" ? null : (
        <input
          className={"input input--mono input--sm" + (problem ? " input--bad" : "")}
          value={text}
          aria-label={`${label}: value`}
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
