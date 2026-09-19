import { pathParts } from "../format";
import type { Override } from "../api";

/**
 * Path de regra com os parâmetros de segmento destacados: ":id" sai em tinta
 * secundária com sublinhado discreto, sem cor de estado, porque é forma do
 * path e não algo que o gateway fez.
 */
export function PathText({ path }: { path: string }) {
  return (
    <>
      {pathParts(path).map((p, i) =>
        p.param ? (
          <span key={i} className="path-param" title={`parâmetro de segmento: ${p.text} casa com qualquer valor neste segmento`}>
            {p.text}
          </span>
        ) : (
          p.text
        ),
      )}
    </>
  );
}

/** O seletor da regra (método e path) com os parâmetros de segmento destacados. */
export function RuleSelector({ o }: { o: Override }) {
  const m = o.match;
  return (
    <>
      {m.method ? m.method + " " : ""}
      {m.pathRegex ? `~${m.pathRegex}` : m.path ? <PathText path={m.path} /> : "*"}
    </>
  );
}
