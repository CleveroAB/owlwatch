import type { ReactNode } from 'react';
import { mountColor, registerMounts } from '../lib/diskSlots';
import { formatBytes, formatBytesTick, formatGiBPair, formatPct, truncateMountPath } from '../lib/format';
import type { HostInfo, Snapshot } from '../lib/types';
import { Meter, meterFlag, type MeterFlag } from './Meter';
import { Sparkline } from './Sparkline';

const WAITING = 'waiting for data…';

interface TilesProps {
  /** Scopes the sticky mount→hue assignment (multi-server hubs). */
  serverId: string;
  host: HostInfo | null;
  latest: Snapshot | null;
  buffer: Snapshot[];
}

export function StatTiles({ serverId, host, latest, buffer }: TilesProps) {
  const showGPU = host?.hasGPU === true || (latest?.gpus.length ?? 0) > 0;
  return (
    <div className="tiles">
      <CpuTile host={host} latest={latest} buffer={buffer} />
      <MemTile latest={latest} buffer={buffer} />
      {showGPU && <GpuTile host={host} latest={latest} buffer={buffer} />}
      <DiskTile serverId={serverId} latest={latest} />
    </div>
  );
}

function StatTile({
  label,
  value,
  sub,
  flag = null,
  children,
}: {
  label: string;
  value: string;
  sub: ReactNode;
  flag?: MeterFlag;
  children?: ReactNode;
}) {
  return (
    <section className="card tile">
      <div className="tile-label">{label}</div>
      <div className="tile-value">{value}</div>
      <div className="tile-sub">
        <span className="tile-sub-text">{sub}</span>
        {flag && (
          <span
            className="tile-flag"
            style={{
              color: flag === 'critical' ? 'var(--status-critical)' : 'var(--status-warn)',
            }}
          >
            ▲ high
          </span>
        )}
      </div>
      {children}
    </section>
  );
}

function CpuTile({ host, latest, buffer }: Omit<TilesProps, 'serverId'>) {
  const cpu = latest?.cpu ?? null;
  const pct = cpu?.usagePct ?? 0;
  const cores = host?.cpuCores ?? cpu?.perCore.length ?? 0;
  return (
    <StatTile
      label="CPU"
      value={cpu ? formatPct(pct) : '—'}
      sub={cpu ? `${cores} cores · load ${cpu.load1.toFixed(2)}` : WAITING}
      flag={cpu ? meterFlag(pct) : null}
    >
      <Sparkline values={buffer.map((s) => s.cpu.usagePct)} color="var(--series-1)" />
      <Meter pct={cpu ? pct : 0} hue="var(--series-1)" />
    </StatTile>
  );
}

function MemTile({ latest, buffer }: Omit<TilesProps, 'serverId' | 'host'>) {
  const mem = latest?.mem ?? null;
  const sub = mem
    ? `of ${formatBytesTick(mem.total)} · ${Math.round(mem.usedPct)}%` +
      (mem.swapUsed > 0 ? ` · swap ${formatBytes(mem.swapUsed)}` : '')
    : WAITING;
  return (
    <StatTile
      label="Memory"
      value={mem ? formatBytes(mem.used) : '—'}
      sub={sub}
      flag={mem ? meterFlag(mem.usedPct) : null}
    >
      <Sparkline values={buffer.map((s) => s.mem.usedPct)} color="var(--series-2)" />
      <Meter pct={mem?.usedPct ?? 0} hue="var(--series-2)" />
    </StatTile>
  );
}

function gpuAvgUtil(s: Snapshot): number | null {
  if (s.gpus.length === 0) return null;
  return s.gpus.reduce((sum, g) => sum + g.utilPct, 0) / s.gpus.length;
}

function GpuTile({ host, latest, buffer }: Omit<TilesProps, 'serverId'>) {
  const gpus = latest?.gpus ?? [];
  const has = gpus.length > 0;
  const avg = has ? gpus.reduce((sum, g) => sum + g.utilPct, 0) / gpus.length : 0;
  let sub: string = WAITING;
  if (has) {
    const name = gpus[0].name || host?.gpuNames[0] || 'GPU';
    const label = gpus.length > 1 ? `${gpus.length}× ${name}` : name;
    const memUsed = gpus.reduce((sum, g) => sum + g.memUsed, 0);
    const memTotal = gpus.reduce((sum, g) => sum + g.memTotal, 0);
    const temp = Math.max(...gpus.map((g) => g.tempC));
    sub = `${label} · ${formatGiBPair(memUsed, memTotal)} · ${Math.round(temp)}°C`;
  }
  return (
    <StatTile
      label="GPU"
      value={has ? formatPct(avg) : '—'}
      sub={sub}
      flag={has ? meterFlag(avg) : null}
    >
      <Sparkline values={buffer.map(gpuAvgUtil)} color="var(--series-5)" />
      <Meter pct={has ? avg : 0} hue="var(--series-5)" />
    </StatTile>
  );
}

/**
 * Disk tile: headline is the fullest mount; below it a mount list (up to 3,
 * fullest first) with mini-meters. Mini-meter hues come from the shared
 * sticky mount→slot assigner (also used by the disk chart) so identity
 * matches across the page and never reshuffles.
 */
function DiskTile({ serverId, latest }: { serverId: string; latest: Snapshot | null }) {
  const disks = latest?.disks ?? [];
  registerMounts(serverId, disks.map((d) => d.mount).sort());
  const byUsage = [...disks].sort((a, b) => b.usedPct - a.usedPct);
  const fullest = byUsage[0] ?? null;
  return (
    <StatTile
      label="Disk"
      value={fullest ? formatPct(fullest.usedPct) : '—'}
      sub={fullest ? `${truncateMountPath(fullest.mount)} · ${formatBytes(fullest.free)} free` : WAITING}
      flag={fullest ? meterFlag(fullest.usedPct) : null}
    >
      {byUsage.length > 0 && (
        <div className="mounts">
          {byUsage.slice(0, 3).map((d) => (
            <div key={d.mount} className="mount-row">
              <span className="mount-name" title={`${d.mount} — ${d.device} (${d.fstype})`}>
                {truncateMountPath(d.mount)}
              </span>
              <span className="mount-pct">{formatPct(d.usedPct)}</span>
              <Meter pct={d.usedPct} hue={mountColor(serverId, d.mount)} />
            </div>
          ))}
        </div>
      )}
    </StatTile>
  );
}
