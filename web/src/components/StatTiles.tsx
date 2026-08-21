import { useEffect, useId, useState, type ReactNode } from 'react';
import { fetchDiskUsage, serverBase } from '../lib/api';
import { mountColor, registerMounts } from '../lib/diskSlots';
import { formatBytes, formatBytesTick, formatGiBPair, formatPct, truncateMountPath } from '../lib/format';
import type { DiskMetrics, DiskUsage, HostInfo, MemMetrics, ProcessMemoryMetrics, Snapshot } from '../lib/types';
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
      details={<DiskDetails serverId={serverId} disks={disks} />}
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

function DiskDetails({ serverId, disks }: { serverId: string; disks: Snapshot['disks'] }) {
  const mounts = largestDisks(disks);
  const fullestMount = [...disks].sort((a, b) => b.usedPct - a.usedPct)[0]?.mount ?? '';
  const [path, setPath] = useState(fullestMount);
  const [usage, setUsage] = useState<DiskUsage | null>(null);
  const [loading, setLoading] = useState(path !== '');
  const [error, setError] = useState(false);

  useEffect(() => {
    if (!path) return;
    const ctrl = new AbortController();
    setLoading(true);
    setError(false);
    setUsage(null);
    fetchDiskUsage(serverBase(serverId), path, ctrl.signal)
      .then(setUsage)
      .catch((err: unknown) => {
        if (!(err instanceof DOMException && err.name === 'AbortError')) setError(true);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });
    return () => ctrl.abort();
  }, [path, serverId]);

  const mount = usage?.mount ?? containingMount(path, disks)?.mount ?? '';
  const canGoUp = path !== '' && mount !== '' && path !== mount;
  return (
    <>
      <div className="disk-browser-head">
        <div>
          <h2>Largest files and directories</h2>
          <p className="disk-browser-path" title={path}>{path || 'No filesystem selected'}</p>
        </div>
        {mounts.length > 0 && (
          <label className="disk-mount-picker">
            <span>Filesystem</span>
            <select value={mount} onChange={(event) => setPath(event.target.value)}>
              {mounts.map((disk) => (
                <option key={disk.mount} value={disk.mount}>
                  {disk.mount} · {formatBytes(disk.used)} used
                </option>
              ))}
            </select>
          </label>
        )}
      </div>
      {canGoUp && (
        <button type="button" className="resource-back" onClick={() => setPath(parentDiskPath(path, mount))}>
          ← Up one level
        </button>
      )}
      {mounts.length === 0 ? (
        <p className="resource-empty">Disk details unavailable.</p>
      ) : loading ? (
        <p className="resource-empty" role="status">Scanning {path} for the largest items…</p>
      ) : error || !usage ? (
        <p className="resource-empty" role="alert">Could not scan this location. It may be unreadable or the server may be offline.</p>
      ) : usage.items.length === 0 ? (
        <p className="resource-empty">No readable files or directories in this location.</p>
      ) : (
        <>
          <div className="resource-table-wrap">
            <table className="resource-table disk-usage-table">
              <thead>
                <tr>
                  <th scope="col">Name</th>
                  <th scope="col">Type</th>
                  <th scope="col">Size</th>
                  <th scope="col">Share of disk usage</th>
                </tr>
              </thead>
              <tbody>
                {usage.items.map((item) => (
                  <tr key={item.path}>
                    <td title={item.path}>
                      {item.kind === 'directory' ? (
                        <button type="button" className="disk-entry-link" onClick={() => setPath(item.path)}>
                          {item.name}<span aria-hidden="true">›</span>
                        </button>
                      ) : item.name}
                    </td>
                    <td>{item.kind === 'directory' ? 'Directory' : 'File'}{item.incomplete ? ' (partial)' : ''}</td>
                    <td>{item.incomplete ? '≥ ' : ''}{formatBytes(item.size)}</td>
                    <td>
                      <div className="resource-share">
                        <span>{formatPct(item.usedPct)}</span>
                        <Meter pct={item.usedPct} hue="var(--series-3)" />
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className={`disk-scan-summary${usage.truncated ? ' disk-scan-warning' : ''}`}>
            Scanned {usage.scannedEntries.toLocaleString()} entries
            {usage.skippedEntries > 0 ? ` · ${usage.skippedEntries.toLocaleString()} unreadable` : ''}
            {usage.truncated ? ' · Partial results: time or entry safety limit reached' : ''}
          </p>
        </>
      )}
    </>
  );
}

export function containingMount(path: string, disks: DiskMetrics[]): DiskMetrics | null {
  return [...disks]
    .filter((disk) => path === disk.mount || path.startsWith(disk.mount === '/' ? '/' : `${disk.mount}/`))
    .sort((a, b) => b.mount.length - a.mount.length)[0] ?? null;
}

export function parentDiskPath(path: string, mount: string): string {
  if (path === mount) return mount;
  const trimmed = path.replace(/\/+$/, '');
  const slash = trimmed.lastIndexOf('/');
  const parent = slash <= 0 ? '/' : trimmed.slice(0, slash);
  return parent.length < mount.length ? mount : parent;
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
