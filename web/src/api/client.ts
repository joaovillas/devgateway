// Cliente REST da API de administração. Cada função corresponde a uma
// operação de docs/api.md, na mesma ordem.
import type {
  ApiErrorBody,
  DeriveDraft,
  DeriveRequest,
  Exchange,
  ExchangeFilter,
  ExchangePage,
  GatewayFilePatch,
  LearningView,
  Override,
  OverrideResource,
  OverrideStateList,
  ReloadResult,
  Route,
  RouteResource,
  SettingsPatchResult,
  SettingsView,
  Status,
  UpstreamHealth,
} from "./types";

/** Erro devolvido pela API, ou falha de rede (code "network"). */
export class ApiError extends Error {
  readonly status: number;
  readonly body: ApiErrorBody;

  constructor(status: number, body: ApiErrorBody) {
    super(body.message);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
  }

  get code(): string {
    return this.body.error;
  }
}

export function isApiError(e: unknown, code?: string): e is ApiError {
  return e instanceof ApiError && (code === undefined || e.code === code);
}

/** Um documento bruto com a versão devolvida em ETag. */
export interface VersionedText {
  text: string;
  etag: string | null;
  /** Falso quando o arquivo ainda não existe em disco (X-Gateway-File-Exists: false). */
  exists: boolean;
}

interface RequestOptions {
  method?: string;
  query?: Record<string, string | number | boolean | undefined>;
  json?: unknown;
  text?: { body: string; contentType: string };
  contentType?: string;
  ifMatch?: string;
  signal?: AbortSignal;
}

const BASE = "/api";

function buildURL(path: string, query?: RequestOptions["query"]): string {
  const qs = new URLSearchParams();
  if (query) {
    for (const [k, v] of Object.entries(query)) {
      if (v !== undefined && v !== "") qs.set(k, String(v));
    }
  }
  const s = qs.toString();
  return BASE + path + (s ? "?" + s : "");
}

async function send(path: string, opts: RequestOptions = {}): Promise<Response> {
  const headers: Record<string, string> = { Accept: "application/json" };
  let body: BodyInit | undefined;
  if (opts.json !== undefined) {
    headers["Content-Type"] = opts.contentType ?? "application/json";
    body = JSON.stringify(opts.json);
  } else if (opts.text) {
    headers["Content-Type"] = opts.text.contentType;
    body = opts.text.body;
  }
  if (opts.ifMatch) headers["If-Match"] = opts.ifMatch;

  let res: Response;
  try {
    res = await fetch(buildURL(path, opts.query), {
      method: opts.method ?? "GET",
      headers,
      body,
      signal: opts.signal,
    });
  } catch (e) {
    if (e instanceof DOMException && e.name === "AbortError") throw e;
    throw new ApiError(0, {
      error: "network",
      message: "sem resposta da porta de administração",
    });
  }
  if (!res.ok) throw await toError(res);
  return res;
}

async function toError(res: Response): Promise<ApiError> {
  const text = await res.text();
  try {
    const parsed = JSON.parse(text) as Partial<ApiErrorBody>;
    if (typeof parsed.error === "string" && typeof parsed.message === "string") {
      return new ApiError(res.status, parsed as ApiErrorBody);
    }
  } catch {
    // corpo não é JSON: cai no erro genérico abaixo
  }
  return new ApiError(res.status, {
    error: "http_" + res.status,
    message: `${res.status} ${res.statusText || "resposta inesperada"} em ${new URL(res.url).pathname}`,
  });
}

async function json<T>(path: string, opts?: RequestOptions): Promise<T> {
  const res = await send(path, opts);
  return (await res.json()) as T;
}

async function none(path: string, opts?: RequestOptions): Promise<void> {
  await send(path, opts);
}

async function versioned(path: string, signal?: AbortSignal): Promise<VersionedText> {
  const res = await send(path, { signal });
  return {
    text: await res.text(),
    etag: res.headers.get("ETag"),
    exists: res.headers.get("X-Gateway-File-Exists") !== "false",
  };
}

const seg = encodeURIComponent;
const MERGE = "application/merge-patch+json";

function filterQuery(f: ExchangeFilter = {}): RequestOptions["query"] {
  return { ...f };
}

export const api = {
  // Processo
  status: (signal?: AbortSignal) => json<Status>("/status", { signal }),

  // Rotas
  listRoutes: (signal?: AbortSignal) =>
    json<{ items: RouteResource[] }>("/routes", { signal }).then((r) => r.items),
  getRoute: (route: string, signal?: AbortSignal) =>
    json<RouteResource>(`/routes/${seg(route)}`, { signal }),
  createRoute: (route: Partial<Route> & Pick<Route, "name" | "match">) =>
    json<RouteResource>("/routes", { method: "POST", json: route }),
  replaceRoute: (name: string, route: Route, ifMatch?: string) =>
    json<RouteResource>(`/routes/${seg(name)}`, { method: "PUT", json: route, ifMatch }),
  patchRoute: (name: string, patch: Record<string, unknown>, ifMatch?: string) =>
    json<RouteResource>(`/routes/${seg(name)}`, {
      method: "PATCH",
      json: patch,
      contentType: MERGE,
      ifMatch,
    }),
  deleteRoute: (name: string, ifMatch?: string) =>
    none(`/routes/${seg(name)}`, { method: "DELETE", ifMatch }),
  getRouteDocument: (name: string, signal?: AbortSignal) =>
    versioned(`/routes/${seg(name)}/document`, signal),
  putRouteDocument: (name: string, yaml: string, ifMatch?: string) =>
    json<RouteResource>(`/routes/${seg(name)}/document`, {
      method: "PUT",
      text: { body: yaml, contentType: "application/yaml" },
      ifMatch,
    }),

  // Overrides
  listOverrides: (route: string, signal?: AbortSignal) =>
    json<{ items: OverrideResource[] }>(`/routes/${seg(route)}/overrides`, { signal }).then(
      (r) => r.items,
    ),
  getOverride: (route: string, name: string, signal?: AbortSignal) =>
    json<OverrideResource>(`/routes/${seg(route)}/overrides/${seg(name)}`, { signal }),
  createOverride: (route: string, override: Override) =>
    json<OverrideResource>(`/routes/${seg(route)}/overrides`, { method: "POST", json: override }),
  replaceOverride: (route: string, name: string, override: Override, ifMatch?: string) =>
    json<OverrideResource>(`/routes/${seg(route)}/overrides/${seg(name)}`, {
      method: "PUT",
      json: override,
      ifMatch,
    }),
  /**
   * Merge patch. Para cumprir "ajustar liga no mesmo gesto", o chamador que
   * mexe em probability, latency ou drop manda enabled: true junto.
   */
  patchOverride: (route: string, name: string, patch: Record<string, unknown>, ifMatch?: string) =>
    json<OverrideResource>(`/routes/${seg(route)}/overrides/${seg(name)}`, {
      method: "PATCH",
      json: patch,
      contentType: MERGE,
      ifMatch,
    }),
  setOverrideEnabled: (route: string, name: string, enabled: boolean) =>
    api.patchOverride(route, name, { enabled }),
  deleteOverride: (route: string, name: string, ifMatch?: string) =>
    none(`/routes/${seg(route)}/overrides/${seg(name)}`, { method: "DELETE", ifMatch }),
  resetOverride: (route: string, name: string) =>
    json<OverrideResource>(`/routes/${seg(route)}/overrides/${seg(name)}/reset`, {
      method: "POST",
    }),
  deriveOverride: (route: string, req: DeriveRequest) =>
    json<DeriveDraft | OverrideResource>(`/routes/${seg(route)}/overrides/derive`, {
      method: "POST",
      json: req,
    }),
  overridesState: (signal?: AbortSignal) => json<OverrideStateList>("/overrides/state", { signal }),

  // Configuração
  settings: (signal?: AbortSignal) => json<SettingsView>("/settings", { signal }),
  patchSettings: (patch: GatewayFilePatch) =>
    json<SettingsPatchResult>("/settings", { method: "PATCH", json: patch, contentType: MERGE }),
  getSettingsDocument: (signal?: AbortSignal) => versioned("/settings/document", signal),
  putSettingsDocument: (text: string, ifMatch?: string) =>
    json<SettingsPatchResult>("/settings/document", {
      method: "PUT",
      text: { body: text, contentType: "application/json" },
      ifMatch,
    }),
  learning: (signal?: AbortSignal) => json<LearningView>("/learning", { signal }),
  setLearning: (enabled: boolean) =>
    json<LearningView>("/learning", { method: "PUT", json: { enabled } }),
  reload: () => json<ReloadResult>("/reload", { method: "POST" }),

  // Histórico
  listExchanges: (
    filter?: ExchangeFilter,
    page?: { limit?: number; cursor?: string },
    signal?: AbortSignal,
  ) =>
    json<ExchangePage>("/exchanges", {
      query: { ...filterQuery(filter), limit: page?.limit, cursor: page?.cursor },
      signal,
    }),
  getExchange: (id: string, signal?: AbortSignal) => json<Exchange>(`/exchanges/${seg(id)}`, { signal }),
  /** Vizinha mais antiga; rejeita com ApiError "no_more" no fim. */
  olderExchange: (id: string, filter?: ExchangeFilter, signal?: AbortSignal) =>
    json<Exchange>(`/exchanges/${seg(id)}/older`, { query: filterQuery(filter), signal }),
  /** Vizinha mais nova; rejeita com ApiError "no_more" no fim. */
  newerExchange: (id: string, filter?: ExchangeFilter, signal?: AbortSignal) =>
    json<Exchange>(`/exchanges/${seg(id)}/newer`, { query: filterQuery(filter), signal }),
  clearExchanges: () => none("/exchanges", { method: "DELETE" }),

  // Upstreams
  upstreams: (signal?: AbortSignal) =>
    json<{ items: UpstreamHealth[] }>("/upstreams", { signal }).then((r) => r.items),
};

/** Decodifica um corpo capturado (base64) para texto UTF-8. */
export function decodeBody(b64: string | undefined): string {
  if (!b64) return "";
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return new TextDecoder("utf-8", { fatal: false }).decode(bytes);
}
