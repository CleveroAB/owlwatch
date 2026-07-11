import { memo, useMemo } from 'react';
import { formatBytes, formatBytesTick, formatPct } from '../lib/format';
import { mountColor, registerMounts } from '../lib/diskSlots';
import { niceByteTicks } from '../lib/geometry';
import type { HistoryPoint, HostInfo, RangeKey } from '../lib/types';
import { ChartCard } from './ChartCard';
import type { ChartSeries, ExtraRow } from './TimeSeriesChart';

const PCT_TICKS = [0, 25, 50, 75, 100];
const pctTick = (v: number) => String(v);

interface HistorySectionProps {
  /** Scopes the sticky mount→hue assignment (multi-server hubs). */
  serverId: string;
  points: HistoryPoint[] | null;
  stale: boolean;
  error: boolean;
  range: RangeKey;
  host: HostInfo | null;
}

/**
 * The four history chart cards. Memoized: props only change identity when a
 * fetch settles or the range/host changes, so live ticks don't re-render the
 * whole chart grid every 2s.
 */
export const HistorySection = memo(function HistorySection({
  serverId,
  points,
  stale,
  error,
  range,
  host,
}: HistorySectionProps) {
  const pts = useMemo(() => points ?? [], [points]);
  const timestamps = useMemo(() => pts.map((p) => p.ts), [pts]);
  const hasGPU = host?.hasGPU === true || pts.some((p) => p.gpuUtilPct != null);

  const emptyMessage =
    error && pts.length === 0 ? 'Waiting for the server…' : undefined; // default otherwise

  const cpuSeries = useMemo<ChartSeries[]>(
    () => [
      {
        id: 'cpu',
        name: 'CPU',
        color: 'var(--series-1)',
        values: pts.map((p) => p.cpuPct),
        format: formatPct,
      },
    ],
    [pts],
  );
  const cpuExtra = useMemo(
    () => (i: number): ExtraRow[] => [{ label: 'Peak', value: formatPct(pts[i].cpuMaxPct) }],
    [pts],
  );

  // Fixed capacity domain from host memory; fall back to the observed peak
  // (rounded up by the tick helper) when host identity hasn't arrived yet.
  const memTotal =
    host?.memTotal ?? pts.reduce((max, p) => Math.max(max, p.memUsed), 0) ?? 0;
  const memTicks = useMemo(() => [0, ...niceByteTicks(memTotal)], [memTotal]);
  const memSeries = useMemo<ChartSeries[]>(
    () => [
      {
        id: 'mem',
        name: 'Used',
        color: 'var(--series-2)',
        values: pts.map((p) => p.memUsed),
        format: formatBytes,
      },
    ],
    [pts],
  );
  const memExtra = useMemo(
    () =>
      (i: number): ExtraRow[] => {
        const p = pts[i];
        const rows: ExtraRow[] = [{ label: 'Percent', value: formatPct(p.memPct) }];
        if (p.swapUsed > 0) rows.push({ label: 'Swap', value: formatBytes(p.swapUsed) });
        return rows;
      },
    [pts],
  );

  const gpuSeries = useMemo<ChartSeries[]>(
    () => [
      {
        id: 'gpu',
        name: 'GPU',
        color: 'var(--series-5)',
        values: pts.map((p) => p.gpuUtilPct ?? null),
        format: formatPct,
      },
    ],
    [pts],
  );
  const gpuExtra = useMemo(
    () =>
      (i: number): ExtraRow[] => {
        const p = pts[i];
        const rows: ExtraRow[] = [];
        if (p.gpuMaxPct != null) rows.push({ label: 'Peak', value: formatPct(p.gpuMaxPct) });
        if (p.gpuTempC != null) rows.push({ label: 'Temp', value: `${Math.round(p.gpuTempC)}°C` });
        if (p.gpuMemUsed != null) rows.push({ label: 'VRAM', value: formatBytes(p.gpuMemUsed) });
        return rows;
      },
    [pts],
  );

  // Disk: one series per mount. Colors come from the shared sticky
  // mount→slot assigner (also used by the disk tile), so a mount keeps its
  // hue across range switches even when other mounts appear or vanish.
  const diskSeries = useMemo<ChartSeries[]>(() => {
    const mounts = new Set<string>();
    for (const p of pts) {
      for (const m of Object.keys(p.disks ?? {})) mounts.add(m);
    }
    const sorted = [...mounts].sort();
    registerMounts(serverId, sorted);
    return sorted.slice(0, 8).map((mount) => ({
      id: mount,
      name: mount,
      color: mountColor(serverId, mount),
      values: pts.map((p) => p.disks?.[mount] ?? null),
      format: formatPct,
    }));
  }, [serverId, pts]);

  return (
    <div className="charts">
      <ChartCard
        title="CPU usage %"
        timestamps={timestamps}
        series={cpuSeries}
        range={range}
        yMax={100}
        yTicks={PCT_TICKS}
        formatYTick={pctTick}
        area
        extraRows={cpuExtra}
        stale={stale}
        emptyMessage={emptyMessage}
      />
      <ChartCard
        title="Memory"
        timestamps={timestamps}
        series={memSeries}
        range={range}
        yMax={Math.max(memTotal, 1)}
        yTicks={memTicks}
        formatYTick={formatBytesTick}
        area
        extraRows={memExtra}
        stale={stale}
        emptyMessage={emptyMessage}
      />
      {hasGPU && (
        <ChartCard
          title="GPU utilization %"
          timestamps={timestamps}
          series={gpuSeries}
          range={range}
          yMax={100}
          yTicks={PCT_TICKS}
          formatYTick={pctTick}
          area
          extraRows={gpuExtra}
          stale={stale}
          emptyMessage={emptyMessage}
        />
      )}
      <ChartCard
        title="Disk usage %"
        timestamps={timestamps}
        series={diskSeries}
        range={range}
        yMax={100}
        yTicks={PCT_TICKS}
        formatYTick={pctTick}
        stale={stale}
        emptyMessage={emptyMessage}
      />
    </div>
  );
});
