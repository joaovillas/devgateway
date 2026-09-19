import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { formatDuration, nice, parseDuration } from "../format";

// Console controls. The continuous ones (frequency and latency) use the
// native <input type="range">: arrows, Page Up/Down, Home/End and screen
// readers work with no extra code. The text ones write when the field loses
// focus or on Enter, and Esc undoes what was typed.

export function Switch({
  checked,
  onChange,
  label,
  disabled,
  title,
  describedBy,
  id,
}: {
  id?: string;
  checked: boolean;
  onChange: (next: boolean) => void;
  /** Accessible name: what the switch turns on. */
  label: string;
  disabled?: boolean;
  title?: string;
  describedBy?: string;
}) {
  return (
    <button
      id={id}
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      aria-describedby={describedBy}
      title={title}
      disabled={disabled}
      className={"switch" + (checked ? " switch--on" : "")}
      onClick={() => onChange(!checked)}
    >
      <span className="switch__knob" aria-hidden="true" />
    </button>
  );
}

interface TextFieldProps {
  value: string;
  onCommit: (next: string) => void;
  /** Every keystroke, for whoever shows the effect of the text before it is written (the forwarding preview). */
  onDraft?: (text: string) => void;
  label: string;
  placeholder?: string;
  mono?: boolean;
  disabled?: boolean;
  /** Message for when the text will not do; the value is not written. */
  validate?: (text: string) => string | null;
  id?: string;
  inputMode?: "numeric" | "text" | "decimal";
  list?: string;
  size?: "sm" | "md";
  describedBy?: string;
}

export function TextField({
  value,
  onCommit,
  onDraft,
  label,
  placeholder,
  mono,
  disabled,
  validate,
  id,
  inputMode,
  list,
  size,
  describedBy,
}: TextFieldProps) {
  const [draft, setDraft] = useState(value);
  const [problem, setProblem] = useState<string | null>(null);
  const focused = useRef(false);
  const errId = useId();

  // A new value from the server replaces the draft, except while typing.
  useEffect(() => {
    if (!focused.current) {
      setDraft(value);
      setProblem(null);
    }
  }, [value]);

  // The draft and whoever follows the typing move together: the forwarding
  // preview changes on every keystroke, and Esc takes it back to the written value.
  const type = (t: string) => {
    setDraft(t);
    onDraft?.(t);
  };

  const commit = () => {
    if (draft === value) {
      setProblem(null);
      return;
    }
    const p = validate?.(draft) ?? null;
    setProblem(p);
    if (!p) onCommit(draft);
  };

  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault();
      commit();
    } else if (e.key === "Escape") {
      if (draft !== value || problem) {
        e.preventDefault();
        type(value);
        setProblem(null);
      }
    }
  };

  return (
    <span className="field">
      <input
        id={id}
        className={"input" + (mono ? " input--mono" : "") + (size === "sm" ? " input--sm" : "") + (problem ? " input--bad" : "")}
        value={draft}
        placeholder={placeholder}
        aria-label={id ? undefined : label}
        aria-invalid={problem ? true : undefined}
        aria-describedby={[problem ? errId : null, describedBy].filter(Boolean).join(" ") || undefined}
        disabled={disabled}
        inputMode={inputMode}
        list={list}
        spellCheck={false}
        autoComplete="off"
        onFocus={() => (focused.current = true)}
        onBlur={() => {
          focused.current = false;
          commit();
        }}
        onChange={(e) => type(e.target.value)}
        onKeyDown={onKey}
      />
      {problem ? (
        <span className="field__problem" id={errId} role="alert">
          {problem}
        </span>
      ) : null}
    </span>
  );
}

/** Optional whole number: empty writes null (removes the key from the document). */
export function IntField({
  value,
  onCommit,
  label,
  min,
  max,
  placeholder,
  disabled,
  id,
  describedBy,
}: {
  value: number | null | undefined;
  onCommit: (next: number | null) => void;
  label: string;
  min?: number;
  max?: number;
  placeholder?: string;
  disabled?: boolean;
  id?: string;
  describedBy?: string;
}) {
  return (
    <TextField
      id={id}
      label={label}
      mono
      size="sm"
      inputMode="numeric"
      value={value == null ? "" : String(value)}
      placeholder={placeholder}
      disabled={disabled}
      describedBy={describedBy}
      validate={(t) => {
        if (t.trim() === "") return null;
        if (!/^-?\d+$/.test(t.trim())) return "use a whole number";
        const n = Number(t);
        if (min !== undefined && n < min) return `minimum ${min}`;
        if (max !== undefined && n > max) return `maximum ${max}`;
        return null;
      }}
      onCommit={(t) => onCommit(t.trim() === "" ? null : Number(t))}
    />
  );
}

/** A Go duration as free text: "150ms", "2s", "1m30s". Empty writes null. */
export function DurationField({
  value,
  onCommit,
  label,
  placeholder,
  id,
  allowEmpty = true,
}: {
  value: string | undefined;
  onCommit: (next: string | null) => void;
  label: string;
  placeholder?: string;
  id?: string;
  allowEmpty?: boolean;
}) {
  return (
    <TextField
      id={id}
      label={label}
      mono
      size="sm"
      value={value ?? ""}
      placeholder={placeholder}
      validate={(t) => {
        if (t.trim() === "") return allowEmpty ? null : "enter a duration";
        return parseDuration(t) === null ? "Go duration: 150ms, 2s, 1m30s" : null;
      }}
      onCommit={(t) => onCommit(t.trim() === "" ? null : t.trim())}
    />
  );
}

/**
 * A group of exclusive options, with arrows to switch (the radio group
 * pattern). With `manual`, the arrows only move the focus and the option
 * takes effect on Space, Enter or a click: for heavy choices, where merely
 * passing over the middle option would already do something (switching the
 * history backend, for instance).
 */
export function Segmented<T extends string>({
  value,
  options,
  onChange,
  label,
  disabled,
  manual,
}: {
  value: T;
  options: { value: T; label: string }[];
  onChange: (v: T) => void;
  label: string;
  disabled?: boolean;
  manual?: boolean;
}) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const idx = Math.max(
    0,
    options.findIndex((o) => o.value === value),
  );
  const move = (e: KeyboardEvent, i: number) => {
    let next = -1;
    if (e.key === "ArrowRight" || e.key === "ArrowDown") next = (i + 1) % options.length;
    else if (e.key === "ArrowLeft" || e.key === "ArrowUp") next = (i - 1 + options.length) % options.length;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = options.length - 1;
    if (next < 0) return;
    e.preventDefault();
    refs.current[next]?.focus();
    if (!manual) onChange(options[next]!.value);
  };
  return (
    <span className="seg" role="radiogroup" aria-label={label}>
      {options.map((o, i) => (
        <button
          key={o.value}
          ref={(el) => {
            refs.current[i] = el;
          }}
          type="button"
          role="radio"
          aria-checked={o.value === value}
          tabIndex={i === idx ? 0 : -1}
          disabled={disabled}
          className={"seg__opt" + (o.value === value ? " seg__opt--on" : "")}
          onClick={() => onChange(o.value)}
          onKeyDown={(e) => move(e, i)}
        >
          {o.label}
        </button>
      ))}
    </span>
  );
}

/**
 * Exclusive options stacked one per line, for choices whose consequence does
 * not fit in one word ("only what comes after /zip" against "the whole path,
 * /zip/…"). The same radio group pattern as Segmented; `text` is the
 * accessible name, because the visible label has parts in mono.
 */
export function Choice<T extends string>({
  value,
  options,
  onChange,
  label,
}: {
  value: T;
  options: { value: T; label: ReactNode; text: string }[];
  onChange: (v: T) => void;
  label: string;
}) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const idx = Math.max(
    0,
    options.findIndex((o) => o.value === value),
  );
  const move = (e: KeyboardEvent, i: number) => {
    let next = -1;
    if (e.key === "ArrowRight" || e.key === "ArrowDown") next = (i + 1) % options.length;
    else if (e.key === "ArrowLeft" || e.key === "ArrowUp") next = (i - 1 + options.length) % options.length;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = options.length - 1;
    if (next < 0) return;
    e.preventDefault();
    refs.current[next]?.focus();
    onChange(options[next]!.value);
  };
  return (
    <span className="choice" role="radiogroup" aria-label={label}>
      {options.map((o, i) => (
        <button
          key={o.value}
          ref={(el) => {
            refs.current[i] = el;
          }}
          type="button"
          role="radio"
          aria-checked={o.value === value}
          aria-label={o.text}
          tabIndex={i === idx ? 0 : -1}
          className={"choice__opt" + (o.value === value ? " choice__opt--on" : "")}
          onClick={() => onChange(o.value)}
          onKeyDown={(e) => move(e, i)}
        >
          <span className="choice__mark" aria-hidden="true" />
          <span className="choice__text">{o.label}</span>
        </button>
      ))}
    </span>
  );
}

/** The debounce wait of a continuous control: short, only to gather the drag. */
export const DRAG_DELAY = 140;

/**
 * The frequency of one effect, 0–100% by dragging, with the number editable
 * beside it. `lead` and `tail` are the words that turn the control into the
 * sentence it belongs to: "responds 503 · on ——— 30 % of calls".
 */
export function FrequencyControl({
  value,
  onChange,
  label,
  id,
  onSettle,
  lead,
  tail,
}: {
  value: number;
  onChange: (p: number, delay?: number) => void;
  label: string;
  id: string;
  /** End of the gesture (release, key up, leaving the control): write now. */
  onSettle?: () => void;
  lead?: ReactNode;
  tail?: ReactNode;
}) {
  const pct = Math.round(value * 1000) / 10;
  return (
    <span className="cont">
      {lead ? (
        <span className="cont__word" aria-hidden="true">
          {lead}
        </span>
      ) : null}
      <input
        id={id}
        type="range"
        className="range"
        min={0}
        max={100}
        step={1}
        value={pct}
        style={{ ["--fill" as string]: `${pct}%` }}
        aria-valuetext={`${pct}%`}
        onChange={(e) => onChange(Number(e.target.value) / 100, DRAG_DELAY)}
        onPointerUp={onSettle}
        onKeyUp={onSettle}
        onBlur={onSettle}
      />
      <span className="cont__num">
        <TextField
          label={`${label}, as a percentage`}
          mono
          size="sm"
          inputMode="decimal"
          value={String(pct)}
          validate={(t) => {
            const n = Number(t.replace(",", "."));
            return t.trim() === "" || Number.isNaN(n) || n < 0 || n > 100 ? "from 0 to 100" : null;
          }}
          onCommit={(t) => onChange(Math.round(Number(t.replace(",", ".")) * 10) / 1000)}
        />
        <span className="cont__unit" aria-hidden="true">
          %
        </span>
      </span>
      {tail ? (
        <span className="cont__word" aria-hidden="true">
          {tail}
        </span>
      ) : null}
    </span>
  );
}

// Latency on a logarithmic scale: position 0 is zero, and 1 to STEPS covers
// 10 ms to 30 s, because the difference between 50 ms and 150 ms matters as
// much as the one between 5 s and 15 s.
const STEPS = 120;
const LAT_MIN = 10;
const LAT_MAX = 30_000;

function posToMs(p: number): number {
  if (p <= 0) return 0;
  return nice(LAT_MIN * (LAT_MAX / LAT_MIN) ** ((p - 1) / (STEPS - 1)));
}

function msToPos(v: number): number {
  if (v <= 0) return 0;
  const p = 1 + ((STEPS - 1) * Math.log(Math.max(v, LAT_MIN) / LAT_MIN)) / Math.log(LAT_MAX / LAT_MIN);
  return Math.min(STEPS, Math.round(p));
}

/** One duration by dragging (log scale from 10 ms to 30 s) and by text beside it. */
export function DurationSlider({
  value,
  onChange,
  label,
  id,
  onSettle,
}: {
  value: string;
  onChange: (d: string, delay?: number) => void;
  label: string;
  id?: string;
  /** End of the gesture: write now what was waiting on the debounce. */
  onSettle?: () => void;
}) {
  const ms = parseDuration(value) ?? 0;
  const pos = msToPos(ms);
  return (
    <span className="cont">
      <input
        id={id}
        type="range"
        className="range range--injected"
        min={0}
        max={STEPS}
        step={1}
        value={pos}
        aria-label={id ? undefined : label}
        style={{ ["--fill" as string]: `${(pos / STEPS) * 100}%` }}
        aria-valuetext={value}
        onChange={(e) => onChange(formatDuration(posToMs(Number(e.target.value))), DRAG_DELAY)}
        onPointerUp={onSettle}
        onKeyUp={onSettle}
        onBlur={onSettle}
      />
      <span className="cont__num cont__num--wide">
        <DurationField
          label={`${label}, as a duration`}
          value={value}
          allowEmpty={false}
          onCommit={(d) => {
            if (d) onChange(d);
          }}
        />
      </span>
    </span>
  );
}

/** Label and control on one form row of the console. */
export function Row({
  label,
  htmlFor,
  children,
  hint,
}: {
  label: ReactNode;
  htmlFor?: string;
  children: ReactNode;
  hint?: ReactNode;
}) {
  return (
    <div className="row">
      {htmlFor ? (
        <label className="row__label" htmlFor={htmlFor}>
          {label}
        </label>
      ) : (
        <span className="row__label">{label}</span>
      )}
      <div className="row__ctl">
        {children}
        {hint ? <span className="row__hint">{hint}</span> : null}
      </div>
    </div>
  );
}
