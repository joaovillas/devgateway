import type { ReactNode } from "react";
import { api, type ApiError, type EffectiveValue } from "../api";
import { useResource } from "../hooks";

/**
 * De onde vem um valor de configuração, lido de GET /api/settings. Serve para
 * dizer ao desenvolvedor exatamente qual chave, arquivo ou variável controla o
 * que ele está vendo.
 */
function useSetting(key: string) {
  const [settings] = useResource((s) => api.settings(s), []);
  if (settings.kind !== "ready") return null;
  return settings.data.values.find((v) => v.key === key) ?? null;
}

function Origin({ value, fallbackEnv, fallbackFile }: { value: EffectiveValue | null; fallbackEnv?: string; fallbackFile?: string }) {
  const env = value?.source.origin === "env" ? (value.source.name ?? value.env) : fallbackEnv;
  if (env) {
    return (
      <>
        pela variável de ambiente <span className="mono">{env}</span>, que vence <span className="mono">gateway.json</span>.
        Para mudar, remova ou altere a variável e reinicie o processo
      </>
    );
  }
  const file = value?.source.origin === "file" ? (value.source.name ?? "gateway.json") : fallbackFile;
  if (file) {
    return (
      <>
        em <span className="mono">{file}</span>
      </>
    );
  }
  if (value?.source.origin === "default") {
    return (
      <>
        pelo valor padrão, porque <span className="mono">gateway.json</span> não declara a chave
      </>
    );
  }
  return null;
}

function Recovery({ keyPath, value, locked }: { keyPath: "expose" | "record"; value: string; locked: boolean }) {
  if (locked) return null;
  return (
    <p>
      Para religar: <span className="mono">history.{keyPath}: {value}</span> em <span className="mono">gateway.json</span>,
      ou <span className="mono">PATCH /api/settings</span> com{" "}
      <span className="mono">{`{"history":{"${keyPath}":${value}}}`}</span>. O painel acompanha a mudança sozinho.
    </p>
  );
}

/** 403 history_disabled: o histórico existe, mas a leitura está desligada. */
export function HistoryDisabled({ error }: { error: ApiError }) {
  const setting = useSetting("history.expose");
  const locked = Boolean(error.body.env) || setting?.locked === true;
  return (
    <div className="state state--disabled" role="status">
      <p className="state__title">Histórico desabilitado</p>
      <p>
        A leitura das trocas está desligada por <span className="mono">history.expose = false</span>
        {" "}
        <Origin value={setting} fallbackEnv={error.body.env} fallbackFile={error.body.file} />. Isto não é uma lista vazia:
        o gateway não expõe o que registra.
      </p>
      <Recovery keyPath="expose" value="true" locked={locked} />
    </div>
  );
}

/** recording: false: a leitura funciona, mas nada novo é gravado. */
export function RecordingOff({ compact, children }: { compact?: boolean; children?: ReactNode }) {
  const setting = useSetting("history.record");
  const origin = <Origin value={setting} />;
  if (compact) {
    return (
      <p className="notice notice--state" role="status">
        <strong>Registro desligado.</strong> <span className="mono">history.record = false</span>
        {setting ? <> {origin}</> : null}: as trocas abaixo são anteriores, e as novas não são gravadas.
      </p>
    );
  }
  return (
    <div className="state state--disabled" role="status">
      <p className="state__title">Registro desligado</p>
      <p>
        As requisições são encaminhadas normalmente, mas nenhuma troca é gravada: <span className="mono">history.record = false</span>
        {setting ? <> {origin}</> : null}. A lista está vazia por isso, não por falta de tráfego.
      </p>
      <Recovery keyPath="record" value="true" locked={setting?.locked === true} />
      {children}
    </div>
  );
}
