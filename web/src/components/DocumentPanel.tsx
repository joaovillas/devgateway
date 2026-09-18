import { api, type RouteResource } from "../api";
import type { Load } from "../hooks";
import { useResource } from "../hooks";
import { Panel } from "./Panel";
import { Empty, Failure, Loading } from "./States";

interface DocumentPanelProps {
  selected: string | null;
  route: RouteResource | undefined;
  /** Muda a cada evento de configuração que toca esta rota, para reler o documento. */
  version: number;
}

/** O documento YAML da rota selecionada, exatamente como está em disco. */
export function DocumentPanel({ selected, route, version }: DocumentPanelProps) {
  return (
    <Panel id="doc" title="documento" sub={route ? <span className="mono">{route.file}</span> : undefined}>
      {selected ? (
        <DocumentBody name={selected} route={route} version={version} />
      ) : (
        <Empty title="Nenhum documento aberto">
          <p>O documento da rota selecionada aparece aqui, como está no disco.</p>
        </Empty>
      )}
    </Panel>
  );
}

function DocumentBody({ name, route, version }: { name: string; route: RouteResource | undefined; version: number }) {
  const [doc, reload] = useResource((signal) => api.getRouteDocument(name, signal), [name, version]);
  return <DocumentView doc={doc} name={name} route={route} onRetry={reload} />;
}

function DocumentView({
  doc,
  name,
  route,
  onRetry,
}: {
  doc: Load<{ text: string }>;
  name: string;
  route: RouteResource | undefined;
  onRetry: () => void;
}) {
  if (doc.kind === "loading") return <Loading what="o documento" />;
  if (doc.kind === "error") {
    return (
      <Failure
        what="o documento"
        request={`GET /api/routes/${name}/document`}
        error={doc.error}
        onRetry={onRetry}
      />
    );
  }
  const lines = doc.data.text.replace(/\n$/, "").split("\n");
  return (
    <>
      {route?.hasComments ? (
        <p className="notice">
          Este documento tem comentários. Uma escrita pela API ou pelo painel reescreve o arquivo e os
          perde, junto com a ordem das chaves.
        </p>
      ) : null}
      <div className="doc" role="region" aria-label={`Documento ${route?.file ?? name}`}>
        <div className="doc__gutter" aria-hidden="true">
          {lines.map((_, i) => (
            <div key={i}>{i + 1}</div>
          ))}
        </div>
        <pre className="doc__text">
          {doc.data.text}
        </pre>
      </div>
    </>
  );
}
