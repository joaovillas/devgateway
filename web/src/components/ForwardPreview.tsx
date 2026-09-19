import type { ReactNode } from "react";
import { destLine, forwarding, type ForwardInput, type Forwarding, type Piece } from "../forward";
import { Choice, Row } from "./Controls";

// A prévia do encaminhamento e as duas chaves que a mudam. Existe porque a
// consequência de "remove prefixo" e "reescreve host" não cabe no nome das
// chaves: sem remover o prefixo, o gateway anexa o path inteiro ao path do
// destino ("/ws" + "/viacep/..." = "/ws/viacep/..."), e um destino externo
// que recebe o Host do app responde 301 para o lugar errado. A prévia mostra
// o resultado antes de gravar, em um exemplo concreto.

/** Um path em pedaços, com o trecho inventado marcado. */
function Pieces({ pieces }: { pieces: Piece[] }) {
  return (
    <>
      {pieces.map((p, i) =>
        p.sample ? (
          <span key={i} className="fwd__ex" title="trecho de exemplo: qualquer valor serve aqui">
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
 * O que o app chama e o que o destino recebe, lado a lado, em uma requisição
 * de exemplo montada como o proxy monta a de verdade.
 */
export function ForwardPreview({ input }: { input: ForwardInput }) {
  const f = forwarding(input);
  return (
    <div className="fwd">
      <p className="fwd__line">
        <span className="fwd__who">seu app</span>
        <span className="fwd__req mono">
          GET {f.appHost}
          <Pieces pieces={f.appPath} />
        </span>
      </p>
      <p className="fwd__line">
        <span className="fwd__who">destino</span>
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
              ? "a URL do destino ainda não serve: http:// ou https://"
              : "sem destino: só as regras deste serviço respondem"}
          </span>
        )}
      </p>
      <PreviewNote f={f} />
    </div>
  );
}

/** A linha que avisa que o caminho é um exemplo, e de onde ele saiu. */
function PreviewNote({ f }: { f: Forwarding }) {
  if (!f.sample) return null;
  return (
    <p className="fwd__note">
      <span className="mono fwd__ex">{f.sample}</span> é só um exemplo:{" "}
      {f.entry.wildcard ? (
        <>
          vale para qualquer path sob <span className="mono">{f.entry.prefix || ""}/</span>
        </>
      ) : f.entry.raw ? (
        "o parâmetro casa com qualquer valor nesse segmento"
      ) : (
        "sem path na entrada, este serviço recebe qualquer requisição"
      )}
      .
    </p>
  );
}

/**
 * As duas chaves do encaminhamento com a consequência no rótulo, em vez de
 * "remove prefixo" e "reescreve host". A prévia acima muda junto.
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
  /** Por que o cadastro decidiu assim, enquanto as chaves não forem tocadas. */
  why?: string | null;
}) {
  const f = forwarding(input);
  const prefix = f.entry.wildcard ? f.entry.prefix : "";
  const destHost = f.dest?.host ?? "";
  const tail: ReactNode = prefix ? (
    <>
      só o que vem depois de <span className="mono">{prefix}</span>
    </>
  ) : (
    "só o que vem depois do prefixo da entrada"
  );
  const full: ReactNode = prefix ? (
    <>
      o path inteiro, <span className="mono">{prefix}/…</span>
    </>
  ) : (
    "o path inteiro, como o seu app mandou"
  );
  return (
    <>
      <Row
        label="o que enviar ao destino"
        hint={
          f.exact ? (
            <>
              a entrada é um path exato: não há prefixo para remover
            </>
          ) : undefined
        }
      >
        <Choice
          label="O que enviar ao destino"
          value={input.stripPrefix ? "tail" : "full"}
          onChange={(v) => onStripPrefix(v === "tail")}
          options={[
            { value: "tail", text: prefix ? `só o que vem depois de ${prefix}` : "só o que vem depois do prefixo da entrada", label: tail },
            { value: "full", text: prefix ? `o path inteiro, ${prefix}/...` : "o path inteiro, como o seu app mandou", label: full },
          ]}
        />
      </Row>
      <Row label="Host enviado" hint="destinos externos, e quase todo HTTPS, exigem o do destino">
        <Choice
          label="Host enviado ao destino"
          value={input.rewriteHost ? "dest" : "app"}
          onChange={(v) => onRewriteHost(v === "dest")}
          options={[
            {
              value: "dest",
              text: destHost ? `o do destino (${destHost})` : "o do destino",
              label: destHost ? (
                <>
                  o do destino (<span className="mono">{destHost}</span>)
                </>
              ) : (
                "o do destino"
              ),
            },
            {
              value: "app",
              text: `o que o seu app mandou (${f.appHost})`,
              label: (
                <>
                  o que o seu app mandou (<span className="mono">{f.appHost}</span>)
                </>
              ),
            },
          ]}
        />
      </Row>
      {why ? <p className="fwd__why">{why}; mude à vontade</p> : null}
    </>
  );
}
