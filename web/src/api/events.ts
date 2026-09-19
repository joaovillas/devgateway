// SSE connection to /api/events: reconnection with a growing wait, a
// heartbeat watchdog and an observable connection state. The native
// EventSource gives up for good when the response is not text/event-stream (a
// 404, a proxy in the middle), so reconnection is done here, always, by the
// same rule.
import type { EventMap, EventName } from "./types";

export type ConnectionState =
  | { kind: "connecting"; attempt: number }
  | { kind: "open"; since: number }
  | { kind: "retrying"; attempt: number; retryAt: number; reason: string };

type Listener<K extends EventName> = (data: EventMap[K]) => void;

const EVENTS: EventName[] = [
  "hello",
  "exchanges",
  "config",
  "overrides",
  "upstreams",
  "history",
  "heartbeat",
];

/** With no event at all for this long, the connection is taken as lost (heartbeat every 15 s). */
const SILENCE_MS = 45_000;
/** A connection that neither opens nor fails (a suspended process, a port that accepts and does not answer). */
const CONNECT_MS = 10_000;
const BACKOFF_MIN_MS = 1_000;
const BACKOFF_MAX_MS = 15_000;

export class EventStream {
  private readonly url: string;
  private es: EventSource | null = null;
  private attempt = 0;
  private retryTimer: number | undefined;
  private silenceTimer: number | undefined;
  private stopped = true;
  private state: ConnectionState = { kind: "connecting", attempt: 0 };
  private readonly stateListeners = new Set<(s: ConnectionState) => void>();
  private readonly listeners = new Map<EventName, Set<Listener<EventName>>>();

  constructor(url = "/api/events") {
    this.url = url;
  }

  start(): void {
    if (!this.stopped) return;
    this.stopped = false;
    window.addEventListener("online", this.retryNow);
    this.open();
  }

  stop(): void {
    this.stopped = true;
    window.removeEventListener("online", this.retryNow);
    window.clearTimeout(this.retryTimer);
    window.clearTimeout(this.silenceTimer);
    this.es?.close();
    this.es = null;
  }

  getState(): ConnectionState {
    return this.state;
  }

  onState(fn: (s: ConnectionState) => void): () => void {
    this.stateListeners.add(fn);
    return () => this.stateListeners.delete(fn);
  }

  on<K extends EventName>(name: K, fn: Listener<K>): () => void {
    let set = this.listeners.get(name);
    if (!set) {
      set = new Set();
      this.listeners.set(name, set);
    }
    set.add(fn as Listener<EventName>);
    return () => set.delete(fn as Listener<EventName>);
  }

  /** Tries again now, without waiting out the interval (used by the button and by the online event). */
  readonly retryNow = (): void => {
    if (this.stopped || this.state.kind === "open") return;
    window.clearTimeout(this.retryTimer);
    this.open();
  };

  private setState(s: ConnectionState): void {
    this.state = s;
    for (const fn of this.stateListeners) fn(s);
  }

  private open(): void {
    this.es?.close();
    this.setState({ kind: "connecting", attempt: this.attempt });
    const es = new EventSource(this.url);
    this.es = es;
    // Without this watchdog, a connection stuck in "connecting" would never fail.
    this.armSilence(CONNECT_MS, `/api/events did not answer within ${CONNECT_MS / 1000} s`);

    es.onopen = () => {
      this.attempt = 0;
      this.setState({ kind: "open", since: Date.now() });
      this.armSilence();
    };
    es.onerror = () => {
      if (this.es !== es) return;
      this.fail("the connection to /api/events dropped or was refused");
    };
    for (const name of EVENTS) {
      es.addEventListener(name, (ev) => {
        this.armSilence();
        let data: unknown;
        try {
          data = JSON.parse((ev as MessageEvent<string>).data);
        } catch {
          return; // malformed event: ignored, the connection carries on
        }
        const set = this.listeners.get(name);
        if (set) for (const fn of set) fn(data as EventMap[EventName]);
      });
    }
  }

  private armSilence(ms = SILENCE_MS, reason = `no event in ${SILENCE_MS / 1000} s`): void {
    window.clearTimeout(this.silenceTimer);
    this.silenceTimer = window.setTimeout(() => this.fail(reason), ms);
  }

  private fail(reason: string): void {
    window.clearTimeout(this.silenceTimer);
    this.es?.close();
    this.es = null;
    if (this.stopped) return;
    const wait = Math.min(BACKOFF_MAX_MS, BACKOFF_MIN_MS * 2 ** this.attempt);
    const jitter = Math.round(wait * 0.2 * Math.random());
    this.attempt++;
    const retryAt = Date.now() + wait + jitter;
    this.setState({ kind: "retrying", attempt: this.attempt, retryAt, reason });
    this.retryTimer = window.setTimeout(() => this.open(), wait + jitter);
  }
}
