import { useId, useState, type ReactNode } from 'react';
import { mountColor, registerMounts } from '../lib/diskSlots';
import { formatBytes, formatBytesTick, formatGiBPair, formatPct, truncateMountPath } from '../lib/format';
import type { DiskMetrics, HostInfo, MemMetrics, ProcessMemoryMetrics, Snapshot } from '../lib/types';
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
  expanded,
  onToggle,
  details,
}: {
  label: string;
  value: string;
  sub: ReactNode;
  flag?: MeterFlag;
  children?: ReactNode;
  expanded?: boolean;
  onToggle?: () => void;
  details?: ReactNode;
}) {
  const detailsId = useId();
  const summary = (
    <>
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
    </>
  );

  if (onToggle) {
    return (
      <section className={`card tile tile-expandable${expanded ? ' tile-expanded' : ''}`}>
        <button
          type="button"
          className="tile-trigger"
          aria-expanded={expanded}
          aria-controls={detailsId}
          onClick={onToggle}
        >
          {summary}
          <span className="tile-chevron" aria-hidden="true">⌄</span>
        </button>
        {expanded && (
          <div id={detailsId} className="resource-details">
            {details}
          </div>
        )}
      </section>
    );
  }

  return (
    <section className="card tile">
      {summary}
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
  const [expanded, setExpanded] = useState(false);
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
      expanded={expanded}
      onToggle={() => setExpanded((open) => !open)}
      details={<MemoryDetails latest={latest} />}
    >
      <Sparkline values={buffer.map((s) => s.mem.usedPct)} color="var(--series-2)" />
      <Meter pct={mem?.usedPct ?? 0} hue="var(--series-2)" />
    </StatTile>
  );
}

function MemoryDetails({ latest }: { latest: Snapshot | null }) {
  const processes = topMemoryProcesses(latest?.mem ?? null);
  return (
    <>
      <h2>Top memory processes</h2>
      {processes.length === 0 ? (
        <p className="resource-empty">Process details unavailable.</p>
      ) : (
        <div className="resource-table-wrap">
          <table className="resource-table">
            <thead>
              <tr>
                <th scope="col">Process</th>
                <th scope="col">PID</th>
                <th scope="col">Memory</th>
                <th scope="col">Share</th>
              </tr>
            </thead>
            <tbody>
              {processes.map((process) => (
                <tr key={process.pid}>
                  <td title={process.name}>{process.name}</td>
                  <td>{process.pid}</td>
                  <td>{formatBytes(process.used)}</td>
                  <td>{formatPct(process.usedPct)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
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
  const [expanded, setExpanded] = useState(false);
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
      expanded={expanded}
      onToggle={() => setExpanded((open) => !open)}
      details={<DiskDetails disks={disks} />}
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

function DiskDetails({ disks }: { disks: Snapshot['disks'] }) {
  const bySpace = largestDisks(disks);
  return (
    <>
      <h2>Largest disks by used space</h2>
      {bySpace.length === 0 ? (
        <p className="resource-empty">Disk details unavailable.</p>
      ) : (
        <div className="resource-table-wrap">
          <table className="resource-table">
            <thead>
              <tr>
                <th scope="col">Mount</th>
                <th scope="col">Device</th>
                <th scope="col">Used</th>
                <th scope="col">Free</th>
                <th scope="col">Capacity</th>
              </tr>
            </thead>
            <tbody>
              {bySpace.map((disk) => (
                <tr key={disk.mount}>
                  <td title={disk.mount}>{truncateMountPath(disk.mount, 42)}</td>
                  <td title={disk.device}>{disk.device}</td>
                  <td>{formatBytes(disk.used)}</td>
                  <td>{formatBytes(disk.free)}</td>
                  <td>{formatPct(disk.usedPct)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

export function topMemoryProcesses(mem: MemMetrics | null, limit = 10): ProcessMemoryMetrics[] {
  return [...(mem?.topProcesses ?? [])]
    .sort((a, b) => b.used - a.used || a.pid - b.pid)
    .slice(0, limit);
}

export function largestDisks(disks: DiskMetrics[], limit = 10): DiskMetrics[] {
  return [...disks]
    .sort((a, b) => b.used - a.used || a.mount.localeCompare(b.mount))
    .slice(0, limit);
}
