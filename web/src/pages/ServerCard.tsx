import { mountColor } from '../lib/diskSlots';
import { formatAgo, formatPct, formatUptime } from '../lib/format';
import type { ServerSummary, Snapshot } from '../lib/types';
import { Meter } from '../components/Meter';
import { Sparkline } from '../components/Sparkline';

function gpuAvgUtil(s: Snapshot): number | null {
  if (s.gpus.length === 0) return null;
  return s.gpus.reduce((sum, g) => sum + g.utilPct, 0) / s.gpus.length;
}

/**
 * One server on the overview grid. The whole card is a link to the server's
 * dashboard. Offline cards dim (the status text — icon + label, never color
 * alone — carries the since-when) and keep their last-known values.
 */
export function ServerCard({ server, spark }: { server: ServerSummary; spark: number[] }) {
  const { id, name, online, lastSeen, host, latest } = server;

  const cpu = latest?.cpu.usagePct ?? null;
  const mem = latest?.mem.usedPct ?? null;
  const fullest =
    latest && latest.disks.length > 0
      ? [...latest.disks].sort((a, b) => b.usedPct - a.usedPct)[0]
      : null;
  const gpu = latest ? gpuAvgUtil(latest) : null;
  const showGPU = host?.hasGPU === true || (latest?.gpus.length ?? 0) > 0;

  // Last-known uptime: while offline the counter freezes at the last snapshot.
  const uptimeSecs = host
    ? (online || lastSeen === 0 ? Date.now() : lastSeen) / 1000 - host.bootTime
    : null;

  return (
    <a className={online ? 'card server-card' : 'card server-card offline'} href={`#/s/${encodeURIComponent(id)}`}>
      <div className="server-card-head">
        <span className="server-identity">
          <span className="server-name">{name}</span>
          {host && host.hostname !== name && <span className="chip">{host.hostname}</span>}
        </span>
        <span className="conn" role="status">
          <span
            className="conn-dot"
            style={{ background: online ? 'var(--status-good)' : 'var(--status-warn)' }}
            aria-hidden="true"
          />
          {online ? 'Live' : lastSeen > 0 ? `Unreachable · ${formatAgo(lastSeen)}` : 'Unreachable'}
        </span>
      </div>
      <div className="server-card-meta">
        {uptimeSecs != null ? `up ${formatUptime(uptimeSecs)}` : 'waiting for data…'}
      </div>
      <div className="card-meters">
        <MeterRow label="CPU" pct={cpu} hue="var(--series-1)" />
        <MeterRow label="Mem" pct={mem} hue="var(--series-2)" />
        <MeterRow
          label="Disk"
          pct={fullest?.usedPct ?? null}
          hue={fullest ? mountColor(id, fullest.mount) : 'var(--series-3)'}
        />
        {showGPU && <MeterRow label="GPU" pct={gpu} hue="var(--series-5)" />}
      </div>
      <Sparkline values={spark} color="var(--series-1)" />
    </a>
  );
}

/** Compact one-line meter: label · bar · current value. */
function MeterRow({ label, pct, hue }: { label: string; pct: number | null; hue: string }) {
  return (
    <>
      <span className="cm-label">{label}</span>
      <Meter pct={pct ?? 0} hue={hue} />
      <span className="cm-val">{pct != null ? formatPct(pct) : '—'}</span>
    </>
  );
}
