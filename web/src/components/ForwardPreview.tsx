import type { ReactNode } from "react";
import { destLine, forwarding, type ForwardInput, type Forwarding, type Piece } from "../forward";
import { Choice, Row } from "./Controls";

// The forwarding preview and the two keys that change it. It exists because the
// consequence of "strip prefix" and "rewrite host" does not fit in the names of
// the keys: without stripping the prefix, the gateway appends the whole path to
// the destination's path ("/ws" + "/zip/..." = "/ws/zip/..."), and an
// external destination that receives the app's Host answers 301 to the wrong
// place. The preview shows the result before saving, in a concrete example.

/** A path in pieces, with the invented fragment marked. */
function Pieces({ pieces }: { pieces: Piece[] }) {
  return (
    <>
      {pieces.map((p, i) =>
        p.sample ? (
          <span key={i} className="fwd__ex" title="sample fragment: any value works here">
            {p.text}
          </span>
        ) : (
          <span key={i}>{p.text}</span>
        ),
      )}
    </>
  );
}

/**
 * What the app calls and what the destination receives, side by side, in a
 * sample request built the way the proxy builds the real one.
 */
export function ForwardPreview({ input }: { input: ForwardInput }) {
  const f = forwarding(input);
  return (
    <div className="fwd">
      <p className="fwd__line">
        <span className="fwd__who">your app</span>
        <span className="fwd__req mono">
          GET {f.appHost}
          <Pieces pieces={f.appPath} />
        </span>
      </p>
      <p className="fwd__line">
        <span className="fwd__who">destination</span>
        {f.dest ? (
          <span className="fwd__req mono" title={destLine(f)}>
            GET <span className="fwd__base">{f.dest.host}{f.dest.base}</span>
            <Pieces pieces={f.dest.rest} />
            <span className="fwd__sep" aria-hidden="true">
              {"  ·  "}
            </span>
            <span className="fwd__host">
              <span className="sr-only">, </span>Host: {f.dest.hostHeader}
            </span>
          </span>
        ) : (
          <span className="fwd__req dim">
            {input.destination.trim()
              ? "the destination URL does not work yet: http:// or https://"
              : "no destination: only this service's rules answer"}
          </span>
        )}
      </p>
      <PreviewNote f={f} />
    </div>
  );
}

/** The line that warns the path is an example, and where it came from. */
function PreviewNote({ f }: { f: Forwarding }) {
  if (!f.sample) return null;
  return (
    <p className="fwd__note">
      <span className="mono fwd__ex">{f.sample}</span> is only an example:{" "}
      {f.entry.wildcard ? (
        <>
          it holds for any path under <span className="mono">{f.entry.prefix || ""}/</span>
        </>
      ) : f.entry.raw ? (
        "the parameter matches any value in that segment"
      ) : (
        "with no path in the entry, this service receives any request"
      )}
      .
    </p>
  );
}

/**
 * The two forwarding keys with the consequence in the label, instead of
 * "strip prefix" and "rewrite host". The preview above changes along with them.
 */
export function ForwardKeys({
  input,
  onStripPrefix,
  onRewriteHost,
  why,
}: {
  input: ForwardInput;
  onStripPrefix: (b: boolean) => void;
  onRewriteHost: (b: boolean) => void;
  /** Why the setup decided this way, while the keys have not been touched. */
  why?: string | null;
}) {
  const f = forwarding(input);
  const prefix = f.entry.wildcard ? f.entry.prefix : "";
  const destHost = f.dest?.host ?? "";
  const tail: ReactNode = prefix ? (
    <>
      only what comes after <span className="mono">{prefix}</span>
    </>
  ) : (
    "only what comes after the entry's prefix"
  );
  const full: ReactNode = prefix ? (
    <>
      the whole path, <span className="mono">{prefix}/…</span>
    </>
  ) : (
    "the whole path, as your app sent it"
  );
  return (
    <div className="fwdkeys">
      {why ? <p className="fwd__why">{why}; change it freely</p> : null}
      <Row
        label="what to send to the destination"
        hint={
          f.exact ? (
            <>
              the entry is an exact path: there is no prefix to strip
            </>
          ) : undefined
        }
      >
        <Choice
          label="What to send to the destination"
          value={input.stripPrefix ? "tail" : "full"}
          onChange={(v) => onStripPrefix(v === "tail")}
          options={[
            { value: "tail", text: prefix ? `only what comes after ${prefix}` : "only what comes after the entry's prefix", label: tail },
            { value: "full", text: prefix ? `the whole path, ${prefix}/...` : "the whole path, as your app sent it", label: full },
          ]}
        />
      </Row>
      <Row label="Host sent" hint="external destinations, and nearly all HTTPS, require the destination's">
        <Choice
          label="Host sent to the destination"
          value={input.rewriteHost ? "dest" : "app"}
          onChange={(v) => onRewriteHost(v === "dest")}
          options={[
            {
              value: "dest",
              text: destHost ? `the destination's (${destHost})` : "the destination's",
              label: destHost ? (
                <>
                  the destination's (<span className="mono">{destHost}</span>)
                </>
              ) : (
                "the destination's"
              ),
            },
            {
              value: "app",
              text: `what your app sent (${f.appHost})`,
              label: (
                <>
                  what your app sent (<span className="mono">{f.appHost}</span>)
                </>
              ),
            },
          ]}
        />
      </Row>
    </div>
  );
}
