// Tipos da API de administração, espelhando docs/api.md e os tipos Go em
// internal/config e internal/exchange. Mudou lá, muda aqui.

/** Duração no formato do Go: "150ms", "2s", "1m30s". */
export type Duration = string;
/** Instante RFC 3339 em UTC. */
export type Instant = string;

// ---------- Erros ----------

export type ApiErrorCode =
  | "bad_request"
  | "bad_cursor"
  | "history_disabled"
  | "not_found"
  | "no_more"
  | "method_not_allowed"
  | "conflict"
  | "locked"
  | "port_unavailable"
  | "backend_unavailable"
  | "stale"
  | "unsupported_media_type"
  | "invalid"
  | "internal";

export interface ApiErrorDetail {
  message: string;
  field?: string;
  file?: string;
  line?: number;
  column?: number;
}

export interface ApiErrorBody extends ApiErrorDetail {
  /** Código estável. Um servidor mais novo pode mandar códigos não listados. */
  error: ApiErrorCode | (string & {});
  env?: string;
  errors?: ApiErrorDetail[];
}

// ---------- Processo ----------

export interface Status {
  version: string;
  schemaVersion: number;
  startedAt: Instant;
  configPath: string;
  routesDir: string;
  ports: { traffic: number; admin: number };
  history: { backend: HistoryBackend; record: boolean; expose: boolean };
  learning: { enabled: boolean };
  routes: number;
}

export type HistoryBackend = "memory" | "ndjson" | "sqlite";

// ---------- Documento de rota (config.Route) ----------

/** config.Matcher: texto é igualdade; o objeto declara exatamente um operador. */
export type Matcher =
  | string
  | { equals?: string; regex?: string; json?: unknown; contains?: string };

/** config.Latency: fixa ("2s") ou intervalo sorteado. */
export type Latency = Duration | { min: Duration; max: Duration };

export interface OverrideMatch {
  path?: string;
  pathRegex?: string;
  method?: string;
  headers?: Record<string, Matcher>;
  query?: Record<string, Matcher>;
  body?: Matcher;
}

export interface Respond {
  status?: number;
  headers?: Record<string, string>;
  /** Texto ou estrutura serializada como JSON. */
  body?: unknown;
}

export interface OverrideSource {
  kind: "learned" | "derived";
  exchange: string;
  at?: Instant;
  bodyIncomplete?: boolean;
}

export interface Override {
  name: string;
  /** Ausente equivale a ligado. */
  enabled?: boolean;
  match: OverrideMatch;
  respond?: Respond;
  /** Ausente equivale a 1.0. */
  probability?: number;
  latency?: Latency;
  drop?: boolean;
  ttl?: Duration;
  maxApplications?: number;
  source?: OverrideSource;
}

export interface RouteMatch {
  host?: string;
  path?: string;
}

export interface Route {
  schemaVersion: number;
  name: string;
  upstream?: string;
  match: RouteMatch;
  stripPrefix?: boolean;
  rewriteHost?: boolean;
  timeout?: Duration;
  overrides?: Override[];
}

/** Estado vivo de um override (sem route/override: vem aninhado). */
export interface OverrideLiveState {
  active: boolean;
  expired: null | "ttl" | "applications";
  registeredAt: Instant;
  ttlRemainingMs: number | null;
  applications: number;
  maxApplications: number | null;
  lastAppliedAt: Instant | null;
}

export interface RouteResource {
  file: string;
  hasComments: boolean;
  order: number;
  route: Route;
  state: Record<string, OverrideLiveState>;
}

export interface OverrideResource {
  route: string;
  order: number;
  override: Override;
  state: OverrideLiveState;
}

export interface OverrideStateItem extends OverrideLiveState {
  route: string;
  override: string;
  enabled: boolean;
}

export interface OverrideStateList {
  now: Instant;
  items: OverrideStateItem[];
}

export interface DeriveRequest {
  exchange: string;
  name?: string;
  save?: boolean;
}

export interface DeriveDraft {
  route: string;
  override: Override;
  warnings: string[];
}

// ---------- Configuração do processo ----------

export type Origin = "env" | "file" | "default";

export interface ValueSource {
  origin: Origin;
  name?: string;
}

export type SettingKey =
  | "ports.traffic"
  | "ports.admin"
  | "seed"
  | "history.backend"
  | "history.path"
  | "history.capacity"
  | "history.record"
  | "history.expose"
  | "capture.maxBodyBytes"
  | "learning.enabled"
  | "routesDir";

export interface EffectiveValue {
  key: SettingKey | (string & {});
  env: string;
  value: unknown;
  source: ValueSource;
  locked: boolean;
}

export interface SettingsView {
  file: { path: string; exists: boolean };
  values: EffectiveValue[];
}

/** config.GatewayFile como merge patch: null remove a chave do arquivo. */
export interface GatewayFilePatch {
  schemaVersion?: number | null;
  ports?: { traffic?: number | null; admin?: number | null } | null;
  seed?: number | null;
  history?: {
    backend?: HistoryBackend | null;
    path?: string | null;
    capacity?: number | null;
    record?: boolean | null;
    expose?: boolean | null;
  } | null;
  capture?: { maxBodyBytes?: number | null } | null;
  routesDir?: string | null;
  learning?: { enabled?: boolean | null } | null;
}

export interface SettingsPatchResult {
  settings: SettingsView;
  applied: string[];
  notes: string[];
}

export interface LearningView {
  enabled: boolean;
  source: ValueSource;
  locked: boolean;
  learned: Record<string, number>;
}

export interface ReloadResult {
  routes: number;
  changed: { routes: string[]; settings: string[] };
  warnings: string[];
}

// ---------- Histórico (exchange.Exchange) ----------

export type Outcome = "upstream" | "synthesized" | "dropped" | "gateway";
export type Intervention = "synthesized" | "delayed" | "dropped";

export interface Message {
  headers?: Record<string, string[]>;
  /** Base64 (o Go serializa []byte assim). Ausente na listagem. */
  body?: string;
  size: number;
  truncated?: boolean;
}

export interface Timing {
  totalMs: number;
  upstreamMs: number;
  injectedMs: number;
  gatewayMs: number;
}

export interface Exchange {
  id: string;
  seq: number;
  start: Instant;
  method: string;
  host: string;
  path: string;
  query?: string;
  clientAddr?: string;
  route?: string;
  upstream?: string;
  /** "rota/override". */
  override?: string;
  interventions?: Intervention[];
  outcome: Outcome;
  dropMode?: "hijack" | "stream_reset";
  error?: string;
  /** Zero quando não houve resposta. */
  status?: number;
  request: Message;
  response: Message;
  timing: Timing;
}

/** exchange.Filter na query string. */
export interface ExchangeFilter {
  route?: string;
  upstream?: string;
  override?: string;
  method?: string;
  path?: string;
  statusMin?: number;
  statusMax?: number;
  intervened?: boolean;
  since?: Instant;
  until?: Instant;
}

export interface ExchangePage {
  items: Exchange[];
  next: string;
  recording: boolean;
  backend: HistoryBackend;
}

// ---------- Upstreams ----------

export interface UpstreamHealth {
  upstream: string;
  routes: string[];
  status: "up" | "down" | "unknown";
  recent: { attempts: number; failures: number };
  lastSuccessAt: Instant | null;
  lastFailureAt: Instant | null;
  lastError: string;
}

// ---------- Eventos SSE ----------

export interface EventMap {
  hello: Status;
  exchanges: { items: Exchange[]; dropped: number };
  config: { cause: "api" | "reload" | "learning"; routes?: string[]; settings?: string[] };
  overrides: OverrideStateList;
  upstreams: { items: UpstreamHealth[] };
  history: { cause: "cleared" | "backend"; backend?: HistoryBackend };
  heartbeat: { now: Instant };
}

export type EventName = keyof EventMap;
