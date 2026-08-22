import { useCallback, useEffect, useRef, useState } from 'react';
import { Header } from '../components/Header';
import { HistorySection } from '../components/HistorySection';
import { RangePicker, RANGE_KEYS } from '../components/RangePicker';
import { StatTiles } from '../components/StatTiles';
import { useHistory } from '../hooks/useHistory';
import { useLive } from '../hooks/useLive';
import type { Theme } from '../hooks/useTheme';
import { rebootServer, type ConnectionState } from '../lib/api';
import { formatClock } from '../lib/format';
import type { RangeKey, ServerSummary } from '../lib/types';

const RANGE_STORAGE_KEY = 'owlwatch-range';

function initialRange(): RangeKey {
  try {
    const stored = localStorage.getItem(RANGE_STORAGE_KEY);
    if (stored && (RANGE_KEYS as string[]).includes(stored)) return stored as RangeKey;
  } catch {
    /* storage unavailable */
  }
  return '1h';
}

/**
 * One server's full dashboard — the v1 page, parameterized by server id
 * (all data flows through /api/servers/{id}/*). Standalone instances render
 * this with id "local" and no hub extras: pixel-identical to v1. Callers
 * key this component by id so a server switch remounts all live state.
 */
export function ServerPage({
  id,
  hub,
  servers,
  theme,
  onToggleTheme,
}: {
  id: string;
  /** True on a multi-server hub — adds the back link + server switcher. */
  hub: boolean;
  servers: ServerSummary[];
  theme: Theme;
  onToggleTheme: () => void;
}) {
  const { status, host, latest, buffer, online, lastSeen } = useLive(id);
  const [range, setRange] = useState<RangeKey>(initialRange);
  const history = useHistory(id, range);

  const changeRange = useCallback((r: RangeKey) => {
    setRange(r);
    try {
      localStorage.setItem(RANGE_STORAGE_KEY, r);
    } catch {
      /* storage unavailable */
    }
  }, []);

  useEffect(() => {
    const configuredName = servers.find((server) => server.id === id)?.name ?? id;
    document.title = `owlwatch · ${host?.hostname ?? configuredName}`;
  }, [host, id, servers]);

  // An offline peer keeps the hub's stream open, but for the viewer the
  // server is unreachable — surface the existing amber reconnecting state.
  const displayStatus: ConnectionState = online ? status : 'reconnecting';
  const lastTs = lastSeen || latest?.ts || 0;

  return (
    <div className="page">
      <Header
        host={host}
        status={displayStatus}
        theme={theme}
        onToggleTheme={onToggleTheme}
        hubNav={hub ? { servers, currentId: id } : undefined}
      />
      <main>
        <h1 className="sr-only">Owlwatch server dashboard</h1>
        <StatTiles serverId={id} host={host} latest={latest} buffer={buffer} />
        <section className="history" aria-label="History">
          <RangePicker value={range} onChange={changeRange} />
          {!online && (
            <p className="chart-notice" role="status">
              <span
                className="conn-dot"
                style={{ background: 'var(--status-warn)' }}
                aria-hidden="true"
              />
              {lastTs
                ? `Server unreachable — last data ${formatClock(lastTs)}`
                : 'Server unreachable — no data received yet'}
            </p>
          )}
          {history.error && (
            <p className="chart-notice" role="alert">
              Could not load {range} history
              {history.range && history.range !== range ? ` — still showing ${history.range}` : ''}. Retrying automatically.
            </p>
          )}
          <HistorySection
            serverId={id}
            points={history.points}
            stale={history.stale}
            error={history.error}
            range={history.range ?? range}
            host={host}
          />
        </section>
      </main>
      <footer className="site-footer">
        <RebootButton id={id} hostname={host?.hostname} status={displayStatus} />
        {host?.version && <span className="footer-version">owlwatch {host.version}</span>}
      </footer>
    </div>
  );
}

type RebootState = 'idle' | 'rebooting' | 'error';

function RebootButton({
  id,
  hostname,
  status,
}: {
  id: string;
  hostname?: string;
  status: ConnectionState;
}) {
  const [state, setState] = useState<RebootState>('idle');
  const [error, setError] = useState('');
  const sawDisconnect = useRef(false);

  useEffect(() => {
    if (state !== 'rebooting') return;
    if (status !== 'open') {
      sawDisconnect.current = true;
    } else if (sawDisconnect.current) {
      setState('idle');
      sawDisconnect.current = false;
    }
  }, [state, status]);

  const reboot = async () => {
    const name = hostname ?? id;
    if (!window.confirm(`Reboot ${name}? Owlwatch will be unavailable for a few seconds.`)) return;

    setError('');
    setState('rebooting');
    sawDisconnect.current = false;
    try {
      await rebootServer(id);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setState('error');
    }
  };

  return (
    <div className="reboot-control">
      <button
        type="button"
        className="reboot-btn"
        disabled={status !== 'open' || state === 'rebooting'}
        onClick={() => void reboot()}
      >
        <PowerIcon />
        {state === 'rebooting' ? 'Rebooting…' : 'Reboot server'}
      </button>
      {state === 'rebooting' && (
        <span className="reboot-feedback" role="status">Waiting for the server to reconnect…</span>
      )}
      {state === 'error' && (
        <span className="reboot-feedback reboot-error" role="alert">{error}</span>
      )}
    </div>
  );
}

function PowerIcon() {
  return (
    <svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" aria-hidden="true">
      <path d="M8 1.5v6" />
      <path d="M4.25 3.7a5.5 5.5 0 1 0 7.5 0" />
    </svg>
  );
}
