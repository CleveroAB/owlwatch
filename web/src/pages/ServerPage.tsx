import { useCallback, useEffect, useState } from 'react';
import { Header } from '../components/Header';
import { HistorySection } from '../components/HistorySection';
import { RangePicker, RANGE_KEYS } from '../components/RangePicker';
import { StatTiles } from '../components/StatTiles';
import { useHistory } from '../hooks/useHistory';
import { useLive } from '../hooks/useLive';
import type { Theme } from '../hooks/useTheme';
import type { ConnectionState } from '../lib/api';
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
    if (host) document.title = `owlwatch · ${host.hostname}`;
  }, [host]);

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
          <HistorySection
            serverId={id}
            points={history.points}
            stale={history.stale}
            error={history.error}
            range={range}
            host={host}
          />
        </section>
      </main>
      {host?.version && <footer className="site-footer">owlwatch {host.version}</footer>}
    </div>
  );
}
