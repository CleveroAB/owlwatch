import { useEffect, useState } from 'react';
import { fetchActions, type Actions } from '../lib/api';

const NONE: Actions = { restart: false, testEmail: false };

/**
 * The mutating controls the serving instance accepts (GET /api/actions).
 * Everything stays hidden until the answer arrives, and on any failure.
 */
export function useActions(): Actions {
  const [actions, setActions] = useState<Actions>(NONE);
  useEffect(() => {
    const ctrl = new AbortController();
    fetchActions(ctrl.signal)
      .then(setActions)
      .catch(() => {
        /* unknown (offline, 401) — keep every action hidden */
      });
    return () => ctrl.abort();
  }, []);
  return actions;
}
