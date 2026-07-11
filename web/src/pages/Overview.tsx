import { useEffect, useState } from 'react';
import { Header } from '../components/Header';
import type { Theme } from '../hooks/useTheme';
import { connectOverview, pushRing, type ConnectionState } from '../lib/api';
import type { ServerSummary } from '../lib/types';
import { ServerCard } from './ServerCard';

/** CPU sparkline length per card (§9.5: 60 points from the live stream). */
const SPARK_CAP = 60;

export type Sparks = Record<string, number[]>;

/**
 * Seed/refresh spark rings from a full fleet state (§9.5). Both the bootstrap
 * /api/servers response and every `servers` resync event carry `recentCpu`
 * (last ≤60 CPU pct, oldest first), so sparklines render on first paint.
 * Preference per server: the accumulated ring when it is at least as long as
 * the seed (a resync must never shrink it), else `recentCpu`, else a single
 * point from `latest`, else empty.
 */
export function mergeSparks(prev: Sparks, servers: ServerSummary[]): Sparks {
  const next: Sparks = {};
  for (const s of servers) {
    const existing = prev[s.id] ?? [];
    const seed = (s.recentCpu ?? []).slice(-SPARK_CAP);
    if (existing.length > 0 && existing.length >= seed.length) next[s.id] = existing;
    else if (seed.length > 0) next[s.id] = seed;
    else if (s.latest) next[s.id] = [s.latest.cpu.usagePct];
    else next[s.id] = [];
  }
  return next;
}

/**
 * Hub overview (§9.5): a live grid of server cards, all fed by ONE
 * /api/overview/live EventSource. The bootstrap /api/servers response
 * renders the first frame; the stream's `servers` event replaces it and
 * `snapshot`/`status` events keep it current.
 */
export function Overview({
  servers: initialServers,
  theme,
  onToggleTheme,
}: {
  servers: ServerSummary[];
  theme: Theme;
  onToggleTheme: () => void;
}) {
  const [status, setStatus] = useState<ConnectionState>('connecting');
  const [servers, setServers] = useState<ServerSummary[]>(initialServers);
  const [sparks, setSparks] = useState<Sparks>(() => mergeSparks({}, initialServers));

  useEffect(() => {
    document.title = 'owlwatch · overview';
  }, []);

  useEffect(() => {
    return connectOverview({
      onState: setStatus,
      onServers: (list) => {
        setServers(list);
        setSparks((prev) => mergeSparks(prev, list));
      },
      onSnapshot: ({ id, snapshot }) => {
        setServers((prev) =>
          prev.map((s) =>
            s.id === id ? { ...s, online: true, lastSeen: snapshot.ts, latest: snapshot } : s,
          ),
        );
        setSparks((prev) => ({
          ...prev,
          [id]: pushRing(prev[id] ?? [], snapshot.cpu.usagePct, SPARK_CAP),
        }));
      },
      onStatus: ({ id, online, lastSeen }) => {
        setServers((prev) => prev.map((s) => (s.id === id ? { ...s, online, lastSeen } : s)));
      },
    });
  }, []);

  return (
    <div className="page">
      <Header host={null} status={status} theme={theme} onToggleTheme={onToggleTheme} />
      <main>
        <section className="overview-grid" aria-label="Servers">
          {servers.map((s) => (
            <ServerCard key={s.id} server={s} spark={sparks[s.id] ?? []} />
          ))}
        </section>
      </main>
    </div>
  );
}
