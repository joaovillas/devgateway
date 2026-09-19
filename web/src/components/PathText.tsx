import { pathParts } from "../format";
import type { Override } from "../api";

/**
 * Rule path with the segment parameters highlighted: ":id" comes out in
 * secondary ink with a discreet underline, with no state color, because it is
 * the shape of the path and not something the gateway did.
 */
export function PathText({ path }: { path: string }) {
  return (
    <>
      {pathParts(path).map((p, i) =>
        p.param ? (
          <span key={i} className="path-param" title={`segment parameter: ${p.text} matches any value in this segment`}>
            {p.text}
          </span>
        ) : (
          p.text
        ),
      )}
    </>
  );
}

/** The rule selector (method and path) with the segment parameters highlighted. */
export function RuleSelector({ o }: { o: Override }) {
  const m = o.match;
  return (
    <>
      {m.method ? m.method + " " : ""}
      {m.pathRegex ? `~${m.pathRegex}` : m.path ? <PathText path={m.path} /> : "*"}
    </>
  );
}
