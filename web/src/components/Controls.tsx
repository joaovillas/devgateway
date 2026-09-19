import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { formatDuration, nice, parseDuration } from "../format";

// Controles do console. Os contínuos (probabilidade e latência) usam o
// <input type="range"> nativo: setas, Page Up/Down, Home/End e leitor de tela
// funcionam sem código extra. Os de texto gravam ao sair do campo ou no Enter,
// e Esc desfaz o que foi digitado.

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
  /** Nome acessível: o que o interruptor liga. */
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
  /** Cada tecla, para quem mostra o efeito do texto antes de gravá-lo (a prévia do encaminhamento). */
  onDraft?: (text: string) => void;
  label: string;
  placeholder?: string;
  mono?: boolean;
  disabled?: boolean;
  /** Mensagem quando o texto não serve; o valor não é gravado. */
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

  // Valor novo vindo do servidor substitui o rascunho, exceto durante a digitação.
  useEffect(() => {
    if (!focused.current) {
      setDraft(value);
      setProblem(null);
    }
  }, [value]);

  // Rascunho e quem acompanha a digitação andam juntos: a prévia do
  // encaminhamento muda a cada tecla, e Esc a devolve ao valor gravado.
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

/** Número inteiro opcional: vazio grava null (remove a chave do documento). */
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
        if (!/^-?\d+$/.test(t.trim())) return "use um número inteiro";
        const n = Number(t);
        if (min !== undefined && n < min) return `mínimo ${min}`;
        if (max !== undefined && n > max) return `máximo ${max}`;
        return null;
      }}
      onCommit={(t) => onCommit(t.trim() === "" ? null : Number(t))}
    />
  );
}

/** Duração do Go em texto livre: "150ms", "2s", "1m30s". Vazio grava null. */
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
        if (t.trim() === "") return allowEmpty ? null : "informe uma duração";
        return parseDuration(t) === null ? "duração do Go: 150ms, 2s, 1m30s" : null;
      }}
      onCommit={(t) => onCommit(t.trim() === "" ? null : t.trim())}
    />
  );
}

/**
 * Grupo de opções exclusivas, com setas para trocar (padrão de radio group).
 * Com `manual`, as setas só movem o foco e a opção vale com Espaço, Enter ou
 * clique: para escolhas de efeito pesado, em que passar pela opção do meio já
 * faria alguma coisa (trocar o backend do histórico, por exemplo).
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
 * Opções exclusivas empilhadas, uma por linha, para escolhas cuja consequência
 * não cabe em uma palavra ("só o que vem depois de /viacep" contra "o path
 * inteiro, /viacep/…"). Mesmo padrão de radio group do Segmented; `text` é o
 * nome acessível, porque o rótulo visível tem partes em mono.
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

/** Espera do debounce de um controle contínuo: curta, só para juntar o arrasto. */
export const DRAG_DELAY = 140;

/** Probabilidade 0–100% por arrasto, com o número editável ao lado. */
export function ProbabilityControl({
  value,
  onChange,
  label,
  id,
  onSettle,
}: {
  value: number;
  onChange: (p: number, delay?: number) => void;
  label: string;
  id: string;
  /** Fim do gesto (soltar, largar a tecla, sair do controle): grava já. */
  onSettle?: () => void;
}) {
  const pct = Math.round(value * 1000) / 10;
  return (
    <span className="cont">
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
          label={`${label}, em porcentagem`}
          mono
          size="sm"
          inputMode="decimal"
          value={String(pct)}
          validate={(t) => {
            const n = Number(t.replace(",", "."));
            return t.trim() === "" || Number.isNaN(n) || n < 0 || n > 100 ? "de 0 a 100" : null;
          }}
          onCommit={(t) => onChange(Math.round(Number(t.replace(",", ".")) * 10) / 1000)}
        />
        <span className="cont__unit" aria-hidden="true">
          %
        </span>
      </span>
    </span>
  );
}

// Latência em escala logarítmica: a posição 0 é zero, e de 1 a STEPS cobre de
// 10 ms a 30 s, porque a diferença entre 50 ms e 150 ms importa tanto quanto
// a entre 5 s e 15 s.
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

/** Uma duração por arrasto (escala log de 10 ms a 30 s) e por texto ao lado. */
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
  /** Fim do gesto: grava já o que esperava o debounce. */
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
          label={`${label}, como duração`}
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

/** Rótulo e controle numa linha de formulário do console. */
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
