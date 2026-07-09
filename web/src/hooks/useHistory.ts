import { useEffect, useRef, useState } from 'react';
import { fetchHistory } from '../lib/api';
import type { HistoryPoint, RangeKey } from '../lib/types';

const REFRESH_MS = 60_000;

export interface HistoryState {
  /** null until the first fetch settles; then always an array. */
  points: HistoryPoint[] | null;
  /** True while a refetch is in flight and stale data is on screen. */
  stale: boolean;
  /** True when the last fetch failed (server unreachable, bad response). */
  error: boolean;
}

/**
 * Fetch /api/history for the given range, refetching every 60s. On range
 * change the in-flight request is aborted and the previous points are kept
 * (marked stale) so the chart can dim instead of flashing empty.
 */
export function useHistory(range: RangeKey): HistoryState {
  const [state, setState] = useState<HistoryState>({ points: null, stale: false, error: false });
  const ctrlRef = useRef<AbortController | null>(null);

  useEffect(() => {
    let disposed = false;

    const load = () => {
      ctrlRef.current?.abort();
      const ctrl = new AbortController();
      ctrlRef.current = ctrl;
      setState((s) => ({ ...s, stale: s.points !== null }));
      fetchHistory(range, ctrl.signal)
        .then((res) => {
          if (disposed || ctrlRef.current !== ctrl) return;
          setState({ points: res.points ?? [], stale: false, error: false });
        })
        .catch(() => {
          if (disposed || ctrl.signal.aborted) return;
          // Keep whatever we had; un-dim so a dead backend doesn't leave a
          // permanently faded chart. The next 60s tick retries.
          setState((s) => ({ points: s.points ?? [], stale: false, error: true }));
        });
    };

    load();
    const timer = window.setInterval(load, REFRESH_MS);
    return () => {
      disposed = true;
      window.clearInterval(timer);
      ctrlRef.current?.abort();
    };
  }, [range]);

  return state;
}
