import { useCallback, useEffect, useState } from 'react';
import { Header } from './components/Header';
import { HistorySection } from './components/HistorySection';
import { RangePicker, RANGE_KEYS } from './components/RangePicker';
import { StatTiles } from './components/StatTiles';
import { useHistory } from './hooks/useHistory';
import { useLive } from './hooks/useLive';
import { useTheme } from './hooks/useTheme';
import type { RangeKey } from './lib/types';

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

export default function App() {
  const { status, host, latest, buffer } = useLive();
  const [theme, toggleTheme] = useTheme();
  const [range, setRange] = useState<RangeKey>(initialRange);
  const history = useHistory(range);

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

  return (
    <div className="page">
      <Header host={host} status={status} theme={theme} onToggleTheme={toggleTheme} />
      <main>
        <StatTiles host={host} latest={latest} buffer={buffer} />
        <section className="history" aria-label="History">
          <RangePicker value={range} onChange={changeRange} />
          <HistorySection
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
