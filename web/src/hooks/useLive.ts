import { useEffect, useState } from 'react';
import { connectLive, fetchHost, pushRing, RING_CAP, type ConnectionState } from '../lib/api';
import type { HostInfo, Snapshot } from '../lib/types';

export interface LiveState {
  status: ConnectionState;
  host: HostInfo | null;
  /** Ring buffer of recent snapshots, oldest first (≤ RING_CAP). */
  buffer: Snapshot[];
  latest: Snapshot | null;
}

/** Subscribe to the live SSE stream for the lifetime of the component. */
export function useLive(): LiveState {
  const [status, setStatus] = useState<ConnectionState>('connecting');
  const [host, setHost] = useState<HostInfo | null>(null);
  const [buffer, setBuffer] = useState<Snapshot[]>([]);

  useEffect(() => {
    const close = connectLive({
      onHello: (hello) => {
        setHost(hello.host);
        // The hello's ring buffer replaces ours — it also backfills any gap
        // after a reconnect.
        setBuffer(hello.recent.slice(-RING_CAP));
      },
      onSnapshot: (snap) => setBuffer((prev) => pushRing(prev, snap)),
      onState: setStatus,
    });

    // Fallback: if the hello event is slow (proxy buffering, first connect),
    // fetch host identity directly. The hello still wins if it arrives first.
    const ctrl = new AbortController();
    const timer = window.setTimeout(() => {
      fetchHost(ctrl.signal)
        .then((h) => setHost((prev) => prev ?? h))
        .catch(() => {
          /* backend not reachable yet — the SSE state already says so */
        });
    }, 2500);

    return () => {
      close();
      ctrl.abort();
      window.clearTimeout(timer);
    };
  }, []);

  return { status, host, buffer, latest: buffer.length > 0 ? buffer[buffer.length - 1] : null };
}
