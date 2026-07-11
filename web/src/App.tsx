import { useCallback, useEffect, useRef, useState } from 'react';
import { TokenGate } from './components/TokenGate';
import { useHashRoute } from './hooks/useHashRoute';
import { useTheme } from './hooks/useTheme';
import { fetchServers, onUnauthorized, setToken, UnauthorizedError } from './lib/api';
import type { ServerSummary } from './lib/types';
import { Overview } from './pages/Overview';
import { ServerPage } from './pages/ServerPage';

/** How often to re-try /api/servers while the backend is unreachable. */
const BOOT_RETRY_MS = 10_000;

/**
 * When the bootstrap call fails outright (backend down/restarting) we behave
 * like v1: render the local dashboard, whose own SSE layer shows the amber
 * reconnecting state, and keep polling /api/servers in the background.
 */
const LOCAL_FALLBACK: ServerSummary[] = [
  { id: 'local', name: 'local', local: true, online: true, lastSeen: 0, intervalMs: 0 },
];

type Boot =
  | { phase: 'loading' }
  | { phase: 'unauthorized' }
  | { phase: 'error' }
  | { phase: 'ready'; servers: ServerSummary[] };

/**
 * Orchestration (§9.5): fetch /api/servers once, gate on 401, then route.
 * A standalone instance (exactly one local entry) always renders the v1
 * dashboard — no overview, no picker, pixel-identical. A hub routes
 * `#/` → overview and `#/s/{id}` → that server's dashboard.
 */
export default function App() {
  const [theme, toggleTheme] = useTheme();
  const route = useHashRoute();
  const [boot, setBoot] = useState<Boot>({ phase: 'loading' });
  const [attempt, setAttempt] = useState(0);
  const [gateFailed, setGateFailed] = useState(false);
  // Only a token the user actually submitted can be "rejected" — a stale
  // stored token on first load shows the plain gate, not the error line.
  const submittedRef = useRef(false);

  // Mid-session 401s (token rotated on the server) surface through the
  // module-level notifier — the hooks that made the request swallow the
  // error. Flip to the token gate; the hash route is untouched, so the
  // retried boot after a successful token save lands on the same view.
  // Already-up gate: the functional update is a no-op, so in-flight requests
  // racing a token save can't loop the gate.
  useEffect(() => {
    onUnauthorized(() => {
      setBoot((b) => (b.phase === 'unauthorized' ? b : { phase: 'unauthorized' }));
    });
    return () => onUnauthorized(null);
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    fetchServers(ctrl.signal)
      .then((servers) => {
        setBoot({ phase: 'ready', servers });
        setGateFailed(false);
      })
      .catch((err) => {
        if (ctrl.signal.aborted) return;
        if (err instanceof UnauthorizedError) {
          setBoot({ phase: 'unauthorized' });
          setGateFailed(submittedRef.current);
        } else {
          setBoot({ phase: 'error' });
        }
      });
    return () => ctrl.abort();
  }, [attempt]);

  // While the backend is unreachable, silently re-try the bootstrap so a hub
  // that comes back up upgrades from the local fallback to the overview.
  useEffect(() => {
    if (boot.phase !== 'error') return;
    const timer = window.setInterval(() => setAttempt((a) => a + 1), BOOT_RETRY_MS);
    return () => window.clearInterval(timer);
  }, [boot.phase]);

  const saveToken = useCallback((token: string) => {
    setToken(token);
    submittedRef.current = true;
    setGateFailed(false);
    setAttempt((a) => a + 1);
  }, []);

  if (boot.phase === 'loading') return null;
  if (boot.phase === 'unauthorized') {
    return <TokenGate failed={gateFailed} onSubmit={saveToken} />;
  }

  const servers = boot.phase === 'ready' ? boot.servers : LOCAL_FALLBACK;
  const standalone = servers.length === 1 && servers[0].local;

  if (standalone) {
    // Route-independent: `#/` and `#/s/local` are the same single dashboard.
    return (
      <ServerPage
        key={servers[0].id}
        id={servers[0].id}
        hub={false}
        servers={servers}
        theme={theme}
        onToggleTheme={toggleTheme}
      />
    );
  }

  if (route.page === 'server' && servers.some((s) => s.id === route.id)) {
    return (
      <ServerPage
        key={route.id}
        id={route.id}
        hub
        servers={servers}
        theme={theme}
        onToggleTheme={toggleTheme}
      />
    );
  }

  // `#/`, plus any unknown server id, lands on the overview.
  return <Overview servers={servers} theme={theme} onToggleTheme={toggleTheme} />;
}
