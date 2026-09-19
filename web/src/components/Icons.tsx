/** Ícones desenhados à mão, traço de 1.5 px, na cor do texto. */
export function CloseIcon() {
  return (
    <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true" focusable="false">
      <path d="M2 2l6 6M8 2l-6 6" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" fill="none" />
    </svg>
  );
}

/** Cadeado: valor travado por variável de ambiente. */
export function LockIcon() {
  return (
    <svg width="10" height="11" viewBox="0 0 10 11" aria-hidden="true" focusable="false" className="icon">
      <rect x="1.25" y="4.75" width="7.5" height="5.5" rx="0.5" stroke="currentColor" strokeWidth="1.5" fill="none" />
      <path d="M3 4.75V3.25a2 2 0 0 1 4 0v1.5" stroke="currentColor" strokeWidth="1.5" fill="none" />
    </svg>
  );
}

/** Seta de abrir e fechar uma seção; gira por CSS quando aberta. */
export function ChevronIcon() {
  return (
    <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true" focusable="false" className="icon icon--chevron">
      <path d="M3.5 2l3 3-3 3" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" fill="none" />
    </svg>
  );
}

/** Mais: acrescentar um item. */
export function PlusIcon() {
  return (
    <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true" focusable="false" className="icon">
      <path d="M5 1.5v7M1.5 5h7" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" fill="none" />
    </svg>
  );
}

/** Seta vertical: navegação para a troca mais nova (up) ou mais antiga (down). */
export function ArrowIcon({ dir }: { dir: "up" | "down" }) {
  return (
    <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true" focusable="false" className="icon">
      <path
        d={dir === "up" ? "M5 8.5V1.5M2 4.5l3-3 3 3" : "M5 1.5v7M2 5.5l3 3 3-3"}
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}

/** Pilha de ajustes: a regra usa algo que só o modo avançado edita. */
export function SlidersIcon() {
  return (
    <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true" focusable="false" className="icon">
      <path d="M1 2.5h8M1 5h8M1 7.5h8" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" fill="none" />
      <circle cx="3.25" cy="2.5" r="1.25" fill="var(--panel)" stroke="currentColor" strokeWidth="1.5" />
      <circle cx="6.75" cy="7.5" r="1.25" fill="var(--panel)" stroke="currentColor" strokeWidth="1.5" />
    </svg>
  );
}

/** Funil: o controle único que abre e fecha os filtros do tráfego. */
export function FilterIcon() {
  return (
    <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true" focusable="false" className="icon">
      <path
        d="M1 1.75h8L6 5.25v3.25L4 7.25V5.25z"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}

/** Triângulo cheio: a marca da intervenção junto do status no tráfego. */
export function MarkIcon() {
  return (
    <svg width="7" height="7" viewBox="0 0 7 7" aria-hidden="true" focusable="false" className="icon">
      <path d="M3.5 0.5l3 6h-6z" fill="currentColor" />
    </svg>
  );
}
