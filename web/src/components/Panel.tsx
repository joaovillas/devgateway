import type { ReactNode } from "react";

interface PanelProps {
  id: string;
  title: string;
  sub?: ReactNode;
  tools?: ReactNode;
  children: ReactNode;
}

/** Fixed console panel: small-caps header and scrollable body. */
export function Panel({ id, title, sub, tools, children }: PanelProps) {
  const headId = `${id}-title`;
  return (
    <section className="panel" aria-labelledby={headId}>
      <header className="panel__head">
        <h2 className="panel__title" id={headId}>
          {title}
        </h2>
        {sub ? <span className="panel__sub">{sub}</span> : null}
        {tools ? <div className="panel__tools">{tools}</div> : null}
      </header>
      <div className="panel__body">{children}</div>
    </section>
  );
}
