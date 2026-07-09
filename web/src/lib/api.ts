/**
 * Data layer: the SSE live stream and the two REST endpoints.
 * All payload types come from ./types (the shared contract).
 */

import type { HelloEvent, HistoryResponse, HostInfo, RangeKey, Snapshot } from './types';

/** Client-side ring buffer capacity — mirrors the collector's 5 min at 2s. */
export const RING_CAP = 150;

export type ConnectionState = 'connecting' | 'open' | 'reconnecting';

export interface LiveHandlers {
  onHello: (hello: HelloEvent) => void;
  onSnapshot: (snap: Snapshot) => void;
  onState: (state: ConnectionState) => void;
}

/** How long to wait before replacing an EventSource the browser gave up on. */
const REOPEN_DELAY_MS = 5000;

/**
 * A stream with no events for this long is dead even if the socket still
 * looks open (half-open connection after laptop sleep, a NAT timeout, or a
 * proxy whose upstream died). This is the pre-hello default; once the hello
 * event reports the server's sample interval the threshold scales to
 * max(STALL_MS, 4 × intervalMs) so slow sample rates (the server's `: ping`
 * comment heartbeat is invisible to EventSource) don't false-disconnect.
 */
const STALL_MS = 15_000;

/**
 * Open the /api/live SSE stream. EventSource retries transient network
 * errors on its own, but two failure modes need manual recovery:
 *
 *  - it gives up for good when the endpoint answers with a non-stream
 *    response (e.g. a reverse proxy returning 502 while the backend
 *    restarts) — a CLOSED stream is recreated after a short delay;
 *  - a half-open connection never errors at all — a stall watchdog redials
 *    when no event has arrived for the stall threshold (STALL_MS until the
 *    hello reports the sample interval, then max(STALL_MS, 4 × intervalMs)).
 *
 * Returns a close function.
 */
export function connectLive(handlers: LiveHandlers): () => void {
  let es: EventSource | null = null;
  let retryTimer: number | null = null;
  let stallTimer: number | null = null;
  let stallMs = STALL_MS;
  let closed = false;

  const clearRetry = () => {
    if (retryTimer !== null) {
      window.clearTimeout(retryTimer);
      retryTimer = null;
    }
  };

  const scheduleReopen = () => {
    if (closed || retryTimer !== null) return;
    retryTimer = window.setTimeout(() => {
      retryTimer = null;
      open();
    }, REOPEN_DELAY_MS);
  };

  const armStallWatchdog = () => {
    if (closed) return;
    if (stallTimer !== null) window.clearTimeout(stallTimer);
    stallTimer = window.setTimeout(() => {
      stallTimer = null;
      handlers.onState('reconnecting');
      open();
    }, stallMs);
  };

  // Replace, never accumulate: whichever path gets here (initial connect,
  // stall watchdog, retry timer) first tears down the old stream and any
  // pending retry, so at most one EventSource is alive at a time.
  const open = () => {
    if (closed) return;
    clearRetry();
    es?.close();
    es = new EventSource('/api/live');
    armStallWatchdog();

    es.onopen = () => handlers.onState('open');
    es.onerror = () => {
      handlers.onState('reconnecting');
      if (es?.readyState === EventSource.CLOSED) scheduleReopen();
    };

    es.addEventListener('hello', (e) => {
      const msg = safeParse<HelloEvent>((e as MessageEvent).data);
      if (msg && Number.isFinite(msg.intervalMs) && msg.intervalMs > 0) {
        stallMs = Math.max(STALL_MS, 4 * msg.intervalMs);
      }
      armStallWatchdog();
      if (msg) handlers.onHello(msg);
    });
    es.addEventListener('snapshot', (e) => {
      armStallWatchdog();
      const msg = safeParse<Snapshot>((e as MessageEvent).data);
      if (msg) handlers.onSnapshot(msg);
    });
  };

  handlers.onState('connecting');
  open();

  return () => {
    closed = true;
    clearRetry();
    if (stallTimer !== null) {
      window.clearTimeout(stallTimer);
      stallTimer = null;
    }
    es?.close();
    es = null;
  };
}

/** Append to a ring buffer, returning a new array capped at `cap`. */
export function pushRing<T>(buf: T[], item: T, cap: number = RING_CAP): T[] {
  const next = buf.length >= cap ? buf.slice(buf.length - cap + 1) : buf.slice();
  next.push(item);
  return next;
}

export async function fetchHistory(range: RangeKey, signal?: AbortSignal): Promise<HistoryResponse> {
  const res = await fetch(`/api/history?range=${range}`, { signal });
  if (!res.ok) throw new Error(`GET /api/history?range=${range}: HTTP ${res.status}`);
  return (await res.json()) as HistoryResponse;
}

export async function fetchHost(signal?: AbortSignal): Promise<HostInfo> {
  const res = await fetch('/api/host', { signal });
  if (!res.ok) throw new Error(`GET /api/host: HTTP ${res.status}`);
  return (await res.json()) as HostInfo;
}

function safeParse<T>(raw: unknown): T | null {
  if (typeof raw !== 'string') return null;
  try {
    return JSON.parse(raw) as T;
  } catch {
    return null;
  }
}
