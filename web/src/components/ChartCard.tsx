import { useMemo, useState } from 'react';
import { formatPointTime } from '../lib/format';
import type { RangeKey } from '../lib/types';
import {
  CHART_HEIGHT,
  TimeSeriesChart,
  type ChartSeries,
  type ExtraRow,
} from './TimeSeriesChart';

export interface ChartCardProps {
  title: string;
  timestamps: number[];
  series: ChartSeries[];
  range: RangeKey;
  yMax: number;
  yTicks: number[];
  formatYTick: (v: number) => string;
  area?: boolean;
  extraRows?: (index: number) => ExtraRow[];
  /** Dim the current render while a refetch is in flight. */
  stale?: boolean;
  emptyMessage?: string;
}

export function ChartCard({
  title,
  timestamps,
  series,
  range,
  yMax,
  yTicks,
  formatYTick,
  area,
  extraRows,
  stale = false,
  emptyMessage = 'Collecting data — check back in a minute.',
}: ChartCardProps) {
  const [view, setView] = useState<'chart' | 'table'>('chart');
  const hasData = timestamps.length >= 2;

  return (
    <section className="card chart-card">
      <header className="chart-head">
        <h2 className="chart-title">{title}</h2>
        <button
          type="button"
          className="icon-btn"
          aria-pressed={view === 'table'}
          aria-label={view === 'chart' ? `Show ${title} as a table` : `Show ${title} as a chart`}
          title={view === 'chart' ? 'Table view' : 'Chart view'}
          onClick={() => setView((v) => (v === 'chart' ? 'table' : 'chart'))}
        >
          {view === 'chart' ? <TableIcon /> : <ChartIcon />}
        </button>
      </header>
      {series.length >= 2 && hasData && (
        <div className="legend">
          {series.map((s) => (
            <span key={s.id} className="legend-item">
              <span className="key" style={{ background: s.color }} />
              {s.name}
            </span>
          ))}
        </div>
      )}
      <div className={stale ? 'chart-body stale' : 'chart-body'}>
        {!hasData ? (
          <div className="empty-msg" style={{ height: CHART_HEIGHT }}>
            {emptyMessage}
          </div>
        ) : view === 'chart' ? (
          <TimeSeriesChart
            timestamps={timestamps}
            series={series}
            range={range}
            yMax={yMax}
            yTicks={yTicks}
            formatYTick={formatYTick}
            area={area}
            extraRows={extraRows}
            ariaLabel={title}
          />
        ) : (
          <DataTable timestamps={timestamps} series={series} range={range} />
        )}
      </div>
    </section>
  );
}

/** Accessibility fallback: the exact chart data as a scrollable table, newest first. */
function DataTable({
  timestamps,
  series,
  range,
}: {
  timestamps: number[];
  series: ChartSeries[];
  range: RangeKey;
}) {
  const rows = useMemo(() => timestamps.map((_, i) => i).reverse(), [timestamps]);
  return (
    <div className="table-wrap" style={{ maxHeight: CHART_HEIGHT }}>
      <table className="data">
        <thead>
          <tr>
            <th scope="col">Time</th>
            {series.map((s) => (
              <th key={s.id} scope="col">
                {s.name}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((i) => (
            <tr key={timestamps[i]}>
              <td>{formatPointTime(timestamps[i], range)}</td>
              {series.map((s) => {
                const v = s.values[i];
                return <td key={s.id}>{v == null ? '—' : s.format(v)}</td>;
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function TableIcon() {
  return (
    <svg width="15" height="15" viewBox="0 0 15 15" fill="none" aria-hidden="true">
      <rect x="1.5" y="2" width="12" height="11" rx="1.5" stroke="currentColor" strokeWidth="1.3" />
      <path d="M1.5 5.5h12M6 5.5V13" stroke="currentColor" strokeWidth="1.3" />
      <path d="M1.5 9.25h12" stroke="currentColor" strokeWidth="1.3" />
    </svg>
  );
}

function ChartIcon() {
  return (
    <svg width="15" height="15" viewBox="0 0 15 15" fill="none" aria-hidden="true">
      <path
        d="M1.5 11.5 5.5 6l3 3.5 4.5-6"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
