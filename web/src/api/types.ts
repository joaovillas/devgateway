// Types of the admin API, mirroring docs/api.md and the Go types in
// internal/config and internal/exchange. When they change there, change here.

/** Duration in Go's format: "150ms", "2s", "1m30s". */
export type Duration = string;
/** RFC 3339 instant in UTC. */
export type Instant = string;

// ---------- Errors ----------

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
  /** Stable code. A newer server may send codes not listed here. */
  error: ApiErrorCode | (string & {});
  env?: string;
  errors?: ApiErrorDetail[];
}

// ---------- Process ----------

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

// ---------- Route document (config.Route) ----------

/** config.Matcher: text is equality; the object declares exactly one operator. */
export type Matcher =
  | string
  | { equals?: string; regex?: string; json?: unknown; contains?: string };

/**
 * config.Latency in its long form: a fixed delay or a range drawn on every
 * request, either of them carrying its own frequency.
 */
export interface LatencySpec {
  fixed?: Duration;
  min?: Duration;
  max?: Duration;
  /** How often the delay is injected, 0.0 to 1.0. Absent means every call. */
  chance?: number;
}

/**
 * config.Latency: the short form is a bare duration ("2s"), which holds on
 * every selected request; the long form carries the frequency.
 */
export type Latency = Duration | LatencySpec;

/**
 * config.Drop: `true` drops every selected request, `false` is the same as not
 * declaring it, and `{ chance }` drops that fraction of them.
 */
export type Drop = boolean | { chance?: number };

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
  /** Text, or a structure serialized as JSON. */
  body?: unknown;
  /** How often the selected requests get this response, 0.0 to 1.0. Absent means all of them. */
  chance?: number;
}

export interface OverrideSource {
  kind: "learned" | "derived";
  exchange: string;
  at?: Instant;
  bodyIncomplete?: boolean;
}

export interface Override {
  name: string;
  /** Absent means on. */
  enabled?: boolean;
  match: OverrideMatch;
  respond?: Respond;
  /**
   * Legacy: it used to be the fraction of the selected requests where the
   * whole override applied. It is still read, as the default frequency of
   * every effect that declares none, and the panel migrates it to per-effect
   * frequencies on the first adjustment.
   */
  probability?: number;
  latency?: Latency;
  drop?: Drop;
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

/** Live state of one override (without route/override: it comes nested). */
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

// ---------- Process configuration ----------

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

/** config.GatewayFile as a merge patch: null removes the key from the file. */
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

// ---------- History (exchange.Exchange) ----------

export type Outcome = "upstream" | "synthesized" | "dropped" | "gateway";
export type Intervention = "synthesized" | "delayed" | "dropped";

export interface Message {
  headers?: Record<string, string[]>;
  /** Base64 (that is how Go serializes []byte). Absent in the listing. */
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
  /** "route/override". */
  override?: string;
  interventions?: Intervention[];
  outcome: Outcome;
  dropMode?: "hijack" | "stream_reset";
  error?: string;
  /** Zero when there was no response. */
  status?: number;
  request: Message;
  response: Message;
  timing: Timing;
}

/** exchange.Filter in the query string. */
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

// ---------- SSE events ----------

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
