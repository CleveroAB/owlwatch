/**
 * Data layer: the SSE streams and the REST endpoints, plus the token that
 * unlocks them when the server runs with OWLWATCH_TOKEN set.
 * All payload types come from ./types (the shared contract).
 *
 * Every server-scoped call goes through /api/servers/{id}/* — those routes
 * exist on standalone instances too (the local server is id "local"), so the
 * UI has exactly one request-building path for both modes.
 */

import type {
  HelloEvent,
  HistoryResponse,
  HostInfo,
  OverviewSnapshotEvent,
  OverviewStatusEvent,
  RangeKey,
  ServerSummary,
  Snapshot,
} from './types';

/** Client-side ring buffer capacity — mirrors the collector's 5 min at 2s. */
export const RING_CAP = 150;

export type ConnectionState = 'connecting' | 'open' | 'reconnecting';

/* ---------- access token ---------- */

const TOKEN_KEY = 'owlwatch-token';

export function getToken(): string | null {
  try {
    return localStorage.getItem(TOKEN_KEY);
  } catch {
    return null; // storage unavailable — requests go out without auth
  }
}

export function setToken(token: string): void {
  try {
    localStorage.setItem(TOKEN_KEY, token);
  } catch {
    /* storage unavailable — the token gate will reappear next load */
  }
}

export function clearToken(): void {
  try {
    localStorage.removeItem(TOKEN_KEY);
  } catch {
    /* storage unavailable */
  }
}

/** Thrown on HTTP 401 so the app can show the token gate instead of an error. */
export class UnauthorizedError extends Error {
  constructor(path: string) {
    super(`GET ${path}: HTTP 401`);
    this.name = 'UnauthorizedError';
  }
}

/**
 * Module-level 401 notifier: most fetches happen deep inside hooks
 * (useHistory, useLive's host fallback) that swallow errors, so a token
 * rotated mid-session would otherwise leave the UI stuck on "Reconnecting…"
 * forever. apiGet reports every 401 here and App resurfaces the token gate.
 */
let unauthorizedListener: (() => void) | null = null;

/** Register the (single) callback invoked on any 401; pass null to clear. */
export function onUnauthorized(listener: (() => void) | null): void {
  unauthorizedListener = listener;
}

/** Base path for one server's API surface: /api/servers/{id}. */
export function serverBase(id: string): string {
  return `/api/servers/${encodeURIComponent(id)}`;
}

/** Headers for authenticated REST and streaming requests. */
function authHeaders(extra?: Record<string, string>): Record<string, string> {
  const token = getToken();
  return token ? { ...extra, Authorization: `Bearer ${token}` } : { ...extra };
}

function reportUnauthorized(token: string | null): void {
  // An old request that finishes after token rotation must not reopen the gate.
  if (getToken() === token) unauthorizedListener?.();
}

/** GET a JSON endpoint with the bearer token attached; 401 → UnauthorizedError. */
async function apiGet<T>(path: string, signal?: AbortSignal): Promise<T> {
  const token = getToken();
  const res = await fetch(path, {
    signal,
    headers: authHeaders(),
  });
  if (res.status === 401) {
    reportUnauthorized(token);
    throw new UnauthorizedError(path);
  }
  if (!res.ok) throw new Error(`GET ${path}: HTTP ${res.status}`);
  return (await res.json()) as T;
}

/* ---------- SSE plumbing (shared by connectLive and connectOverview) ---------- */

/** How long to wait before reopening a failed authenticated SSE fetch. */
const REOPEN_DELAY_MS = 5000;

/**
 * A stream with no events for this long is dead even if the socket still
 * looks open (half-open connection after laptop sleep, a NAT timeout, or a
 * proxy whose upstream died). This is the default; streams that learn the
 * server's sample interval raise the threshold to max(STALL_MS, 4 ×
 * intervalMs) so slow sample rates do not false-disconnect.
 */
const STALL_MS = 15_000;

interface StreamSpec {
  path: string;
  onState: (state: ConnectionState) => void;
  /**
   * Named-event handlers. Every listed event re-arms the stall watchdog
   * before its handler runs; a handler may call setStallMs (floored at
   * STALL_MS) once it learns the stream's expected cadence.
   */
  events: Record<string, (raw: string, setStallMs: (ms: number) => void) => void>;
}

/**
 * Open an authenticated Fetch-based SSE stream with manual recovery for:
 *
 *  - it gives up for good when the endpoint answers with a non-stream
 *    response (e.g. a reverse proxy returning 502 while the backend
 *    restarts) — a CLOSED stream is recreated after a short delay;
 *  - a half-open connection never errors at all — a stall watchdog redials
 *    when no event has arrived for the stall threshold.
 *
 * Returns a close function.
 */
function connectStream({ path, onState, events }: StreamSpec): () => void {
  let ctrl: AbortController | null = null;
  let retryTimer: number | null = null;
  let stallTimer: number | null = null;
  let stallMs = STALL_MS;
  let generation = 0;
  let closed = false;

  const clearTimers = () => {
    if (retryTimer !== null) window.clearTimeout(retryTimer);
    if (stallTimer !== null) window.clearTimeout(stallTimer);
    retryTimer = stallTimer = null;
  };

  const scheduleReopen = () => {
    if (closed || retryTimer !== null) return;
    retryTimer = window.setTimeout(() => {
      retryTimer = null;
      void open();
    }, REOPEN_DELAY_MS);
  };

  const armStallWatchdog = () => {
    if (closed) return;
    if (stallTimer !== null) window.clearTimeout(stallTimer);
    stallTimer = window.setTimeout(() => {
      stallTimer = null;
      onState('reconnecting');
      ctrl?.abort();
    }, stallMs);
  };

  const setStallMs = (ms: number) => {
    stallMs = Math.max(STALL_MS, ms);
    armStallWatchdog();
  };

  const consume = async (body: ReadableStream<Uint8Array>) => {
    const reader = body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    let eventName = '';
    let data: string[] = [];

    const processLine = (raw: string) => {
      const line = raw.endsWith('\r') ? raw.slice(0, -1) : raw;
      if (line === '') {
        if (data.length > 0) events[eventName]?.(data.join('\n'), setStallMs);
        eventName = '';
        data = [];
      } else if (line.startsWith('event:')) {
        eventName = line.slice(6).trimStart();
      } else if (line.startsWith('data:')) {
        data.push(line.slice(5).trimStart());
      }
    };

    try {
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        armStallWatchdog();
        buffer += decoder.decode(value, { stream: true });
        if (buffer.length > 1 << 20) throw new Error('event stream buffer limit exceeded');
        let newline = buffer.indexOf('\n');
        while (newline >= 0) {
          processLine(buffer.slice(0, newline));
          buffer = buffer.slice(newline + 1);
          newline = buffer.indexOf('\n');
        }
      }
    } finally {
      reader.releaseLock();
    }
  };

  const open = async () => {
    if (closed) return;
    ctrl?.abort();
    const current = ++generation;
    const request = new AbortController();
    ctrl = request;
    const token = getToken();
    armStallWatchdog();
    try {
      const res = await fetch(path, {
        signal: request.signal,
        headers: authHeaders({ Accept: 'text/event-stream', 'Cache-Control': 'no-cache' }),
        cache: 'no-store',
      });
      if (closed || current !== generation) return;
      if (res.status === 401) {
        reportUnauthorized(token);
        throw new UnauthorizedError(path);
      }
      const contentType = res.headers.get('Content-Type')?.toLowerCase() ?? '';
      if (!res.ok || !contentType.startsWith('text/event-stream') || !res.body) {
        throw new Error(`GET ${path}: invalid event stream response (${res.status})`);
      }
      onState('open');
      await consume(res.body);
    } catch (err) {
      if (closed || current !== generation) return;
      if (err instanceof UnauthorizedError) return;
      onState('reconnecting');
    } finally {
      if (!closed && current === generation) scheduleReopen();
    }
  };

  onState('connecting');
  void open();
  return () => {
    closed = true;
    generation += 1;
    clearTimers();
    ctrl?.abort();
    ctrl = null;
  };
}

/* ---------- per-server live stream ---------- */

/**
 * Peer reachability notice on a per-server live stream. Sent by a hub when
 * the upstream peer is offline (DESIGN.md §9.6); local streams never emit it.
 */
export interface LiveStatusEvent {
  online: boolean;
  lastSeen?: number; // unix ms of the peer's last snapshot, when known
}

export interface LiveHandlers {
  onHello: (hello: HelloEvent) => void;
  onSnapshot: (snap: Snapshot) => void;
  onState: (state: ConnectionState) => void;
  /** Optional: peer went offline/online upstream (hub-served peers only). */
  onStatus?: (status: LiveStatusEvent) => void;
}

/**
 * Open the {basePath}/live SSE stream (basePath from serverBase()). Parses
 * `hello` and `snapshot` events, plus the hub's `status` events for peers.
 * The stall threshold scales to the sample interval the hello reports.
 */
export function connectLive(handlers: LiveHandlers, basePath: string): () => void {
  return connectStream({
    path: `${basePath}/live`,
    onState: handlers.onState,
    events: {
      hello: (raw, setStallMs) => {
        const msg = safeParse<HelloEvent>(raw);
        if (msg && Number.isFinite(msg.intervalMs) && msg.intervalMs > 0) {
          setStallMs(4 * msg.intervalMs);
        }
        if (msg) handlers.onHello(msg);
      },
      snapshot: (raw) => {
        const msg = safeParse<Snapshot>(raw);
        if (msg) handlers.onSnapshot(msg);
      },
      status: (raw) => {
        const msg = safeParse<LiveStatusEvent>(raw);
        if (msg && typeof msg.online === 'boolean') handlers.onStatus?.(msg);
      },
    },
  });
}

/* ---------- fleet overview stream ---------- */

export interface OverviewHandlers {
  onServers: (servers: ServerSummary[]) => void;
  onSnapshot: (event: OverviewSnapshotEvent) => void;
  onStatus: (event: OverviewStatusEvent) => void;
  onState: (state: ConnectionState) => void;
}

/**
 * Open the /api/overview/live SSE mux: one `servers` event with full fleet
 * state on connect, then `snapshot` and `status` events per server. Same
 * reconnect + stall discipline as connectLive; the stall threshold scales
 * to the fastest sample interval the fleet reports.
 */
export function connectOverview(handlers: OverviewHandlers): () => void {
  return connectStream({
    path: '/api/overview/live',
    onState: handlers.onState,
    events: {
      servers: (raw, setStallMs) => {
        const msg = safeParse<ServerSummary[]>(raw);
        if (!msg) return;
        const intervals = msg.map((s) => s.intervalMs).filter((v) => Number.isFinite(v) && v > 0);
        if (intervals.length > 0) setStallMs(4 * Math.min(...intervals));
        handlers.onServers(msg);
      },
      snapshot: (raw) => {
        const msg = safeParse<OverviewSnapshotEvent>(raw);
        if (msg) handlers.onSnapshot(msg);
      },
      status: (raw) => {
        const msg = safeParse<OverviewStatusEvent>(raw);
        if (msg) handlers.onStatus(msg);
      },
    },
  });
}

/* ---------- ring buffer + REST ---------- */

/** Append to a ring buffer, returning a new array capped at `cap`. */
export function pushRing<T>(buf: T[], item: T, cap: number = RING_CAP): T[] {
  const next = buf.length >= cap ? buf.slice(buf.length - cap + 1) : buf.slice();
  next.push(item);
  return next;
}

/** The boot call: every monitored server, local first (DESIGN.md §9.4). */
export async function fetchServers(signal?: AbortSignal): Promise<ServerSummary[]> {
  return apiGet<ServerSummary[]>('/api/servers', signal);
}

export async function fetchHistory(
  basePath: string,
  range: RangeKey,
  signal?: AbortSignal,
): Promise<HistoryResponse> {
  return apiGet<HistoryResponse>(`${basePath}/history?range=${range}`, signal);
}

export async function fetchHost(basePath: string, signal?: AbortSignal): Promise<HostInfo> {
  return apiGet<HostInfo>(`${basePath}/host`, signal);
}

function safeParse<T>(raw: unknown): T | null {
  if (typeof raw !== 'string') return null;
  try {
    return JSON.parse(raw) as T;
  } catch {
    return null;
  }
}
