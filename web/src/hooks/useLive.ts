import { useEffect, useState } from 'react';
import {
  connectLive,
  fetchHost,
  pushRing,
  RING_CAP,
  serverBase,
  type ConnectionState,
} from '../lib/api';
import type { HostInfo, Snapshot } from '../lib/types';

export interface LiveState {
  status: ConnectionState;
  host: HostInfo | null;
  /** Ring buffer of recent snapshots, oldest first (≤ RING_CAP). */
  buffer: Snapshot[];
  latest: Snapshot | null;
  /**
   * Upstream reachability. Local servers are always online; for a
   * hub-served peer this reflects the hub's `status` events (and flips back
   * to true when snapshots flow).
   */
  online: boolean;
  /** Unix ms of the server's last snapshot; 0 = unknown. */
  lastSeen: number;
}

/** Subscribe to one server's live SSE stream for the lifetime of the component. */
export function useLive(serverId: string): LiveState {
  const [status, setStatus] = useState<ConnectionState>('connecting');
  const [host, setHost] = useState<HostInfo | null>(null);
  const [buffer, setBuffer] = useState<Snapshot[]>([]);
  const [reach, setReach] = useState<{ online: boolean; lastSeen: number }>({
    online: true,
    lastSeen: 0,
  });

  useEffect(() => {
    const base = serverBase(serverId);
    // Reset in case the caller reuses one mount across ids (pages key by id,
    // so this is normally the initial state anyway).
    setStatus('connecting');
    setHost(null);
    setBuffer([]);
    setReach({ online: true, lastSeen: 0 });

    const close = connectLive(
      {
        onHello: (hello) => {
          setHost(hello.host);
          // The hello's ring buffer replaces ours — it also backfills any gap
          // after a reconnect.
          setBuffer(hello.recent.slice(-RING_CAP));
        },
        onSnapshot: (snap) => {
          setBuffer((prev) => pushRing(prev, snap));
          // A flowing snapshot means the upstream peer is back regardless of
          // any earlier offline notice (events arrive in order on one stream).
          setReach({ online: true, lastSeen: snap.ts });
        },
        onStatus: (s) =>
          setReach((prev) => ({
            online: s.online,
            lastSeen: s.lastSeen ?? prev.lastSeen,
          })),
        onState: setStatus,
      },
      base,
    );

    // Fallback: if the hello event is slow (proxy buffering, first connect),
    // fetch host identity directly. The hello still wins if it arrives first.
    const ctrl = new AbortController();
    const timer = window.setTimeout(() => {
      fetchHost(base, ctrl.signal)
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
  }, [serverId]);

  return {
    status,
    host,
    buffer,
    latest: buffer.length > 0 ? buffer[buffer.length - 1] : null,
    online: reach.online,
    lastSeen: reach.lastSeen,
  };
}
