import {
  useCallback,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent,
} from 'react';
import { formatAxisTick, formatPointTime } from '../lib/format';
import { areaPath, linePath, nearestIndex, pickTimeTicks } from '../lib/geometry';
import type { RangeKey } from '../lib/types';
import { useSize } from '../hooks/useSize';

export interface ChartSeries {
  id: string;
  name: string;
  /** CSS color, e.g. 'var(--series-1)'. */
  color: string;
  /** Aligned with the chart's timestamps; null = gap. */
  values: Array<number | null>;
  format: (v: number) => string;
}

export interface ExtraRow {
  label: string;
  value: string;
}

export interface TimeSeriesChartProps {
  timestamps: number[];
  series: ChartSeries[];
  range: RangeKey;
  /** Y domain is always [0, yMax] — percent charts pass 100, byte charts the capacity. */
  yMax: number;
  /** Ascending tick values; 0 renders as the baseline. */
  yTicks: number[];
  formatYTick: (v: number) => string;
  /** Area fill under the line — single-series charts only. */
  area?: boolean;
  /** Additional tooltip rows for a bucket (peak, temp, …). */
  extraRows?: (index: number) => ExtraRow[];
  ariaLabel: string;
}

const MT = 10;
const MR = 12;
const MB = 24;
const PLOT_H = 190;
/** Total rendered height — ChartCard uses it for the table and empty states too. */
export const CHART_HEIGHT = MT + PLOT_H + MB;

export function TimeSeriesChart({
  timestamps,
  series,
  range,
  yMax,
  yTicks,
  formatYTick,
  area = false,
  extraRows,
  ariaLabel,
}: TimeSeriesChartProps) {
  const { ref, width } = useSize<HTMLDivElement>();
  const [active, setActive] = useState<number | null>(null);
  const tipRef = useRef<HTMLDivElement>(null);

  const n = timestamps.length;
  const t0 = timestamps[0] ?? 0;
  const t1 = timestamps[n - 1] ?? 1;
  const span = Math.max(1, t1 - t0);

  // Left margin adapts to the widest tick label (~6.6px per character at 11px).
  const ml = useMemo(() => {
    const longest = yTicks.reduce((w, t) => Math.max(w, formatYTick(t).length), 1);
    return Math.round(longest * 6.6) + 14;
  }, [yTicks, formatYTick]);

  const pw = Math.max(1, width - ml - MR);
  const xFor = useCallback(
    (ts: number) => ml + ((ts - t0) / span) * pw,
    [ml, t0, span, pw],
  );
  const yFor = useCallback(
    (v: number) => MT + PLOT_H - (Math.min(Math.max(v, 0), yMax) / Math.max(yMax, 1e-9)) * PLOT_H,
    [yMax],
  );

  const xs = useMemo(() => timestamps.map(xFor), [timestamps, xFor]);
  const xTicks = useMemo(() => pickTimeTicks(t0, t1), [t0, t1]);

  const paths = useMemo(
    () =>
      series.map((s) => {
        const ys = s.values.map((v) => (v == null ? null : yFor(v)));
        let lastIdx = -1;
        for (let i = s.values.length - 1; i >= 0; i--) {
          if (s.values[i] != null) {
            lastIdx = i;
            break;
          }
        }
        return {
          line: linePath(xs, ys),
          area: area ? areaPath(xs, ys, MT + PLOT_H) : null,
          lastIdx,
        };
      }),
    [series, xs, yFor, area],
  );

  const onPointerMove = (e: PointerEvent<SVGSVGElement>) => {
    if (n === 0) return;
    const rect = e.currentTarget.getBoundingClientRect();
    const px = e.clientX - rect.left;
    const t = t0 + ((px - ml) / pw) * span;
    setActive(nearestIndex(timestamps, t));
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (n === 0) return;
    switch (e.key) {
      case 'ArrowLeft':
      case 'ArrowRight': {
        e.preventDefault();
        const delta = e.key === 'ArrowLeft' ? -1 : 1;
        setActive((a) => Math.min(n - 1, Math.max(0, (a ?? n - 1) + delta)));
        break;
      }
      case 'Home':
        e.preventDefault();
        setActive(0);
        break;
      case 'End':
        e.preventDefault();
        setActive(n - 1);
        break;
      case 'Escape':
        setActive(null);
        break;
    }
  };

  // Position the tooltip beside the crosshair, flipping sides past mid-plot
  // and clamping to the card. Runs after every render that shows a tooltip.
  useLayoutEffect(() => {
    const tip = tipRef.current;
    if (!tip || active == null || active >= n) return;
    const x = xs[active];
    const flip = x > ml + pw * 0.55;
    let left = flip ? x - tip.offsetWidth - 14 : x + 14;
    left = Math.max(2, Math.min(left, width - tip.offsetWidth - 2));
    tip.style.left = `${left}px`;
    tip.style.top = `${MT + 6}px`;
  });

  const activeValid = active != null && active < n;

  return (
    <div
      ref={ref}
      className="chart-wrap"
      style={{ height: CHART_HEIGHT }}
      tabIndex={0}
      role="img"
      aria-label={`${ariaLabel}. Focus the chart and use the left and right arrow keys to inspect values; the table view holds the same data.`}
      onKeyDown={onKeyDown}
      onFocus={() => setActive((a) => a ?? (n > 0 ? n - 1 : null))}
      onBlur={() => setActive(null)}
    >
      {width > 40 && n >= 2 && (
        <>
          <svg
            width={width}
            height={CHART_HEIGHT}
            onPointerMove={onPointerMove}
            onPointerLeave={() => setActive(null)}
          >
            {yTicks.map((t) => {
              const y = yFor(t);
              return (
                <g key={t}>
                  <line
                    x1={ml}
                    x2={ml + pw}
                    y1={y}
                    y2={y}
                    stroke={t === 0 ? 'var(--baseline)' : 'var(--grid)'}
                    strokeWidth={1}
                    shapeRendering="crispEdges"
                  />
                  <text className="axis-tick" x={ml - 8} y={y} dy="0.32em" textAnchor="end">
                    {formatYTick(t)}
                  </text>
                </g>
              );
            })}
            {xTicks.map((t) => (
              <text
                key={t}
                className="axis-tick"
                x={xFor(t)}
                y={CHART_HEIGHT - 7}
                textAnchor="middle"
              >
                {formatAxisTick(t, range)}
              </text>
            ))}
            {paths.map(
              (p, i) =>
                p.area && <path key={series[i].id} d={p.area} fill={series[i].color} opacity={0.1} />,
            )}
            {paths.map((p, i) => (
              <path
                key={series[i].id}
                d={p.line}
                fill="none"
                stroke={series[i].color}
                strokeWidth={2}
                strokeLinejoin="round"
                strokeLinecap="round"
              />
            ))}
            {activeValid && (
              <line
                x1={xs[active]}
                x2={xs[active]}
                y1={MT}
                y2={MT + PLOT_H}
                stroke="var(--baseline)"
                strokeWidth={1}
              />
            )}
            {activeValid &&
              series.map((s) => {
                const v = s.values[active];
                return v == null ? null : (
                  <circle
                    key={s.id}
                    cx={xs[active]}
                    cy={yFor(v)}
                    r={3}
                    fill={s.color}
                    stroke="var(--surface)"
                    strokeWidth={2}
                  />
                );
              })}
            {paths.map((p, i) => {
              if (p.lastIdx < 0) return null;
              const v = series[i].values[p.lastIdx];
              return v == null ? null : (
                <circle
                  key={series[i].id}
                  cx={xs[p.lastIdx]}
                  cy={yFor(v)}
                  r={4}
                  fill={series[i].color}
                  stroke="var(--surface)"
                  strokeWidth={2}
                />
              );
            })}
          </svg>
          {activeValid && (
            <div ref={tipRef} className="chart-tooltip">
              <div className="tt-time">{formatPointTime(timestamps[active], range)}</div>
              {series.map((s) => {
                const v = s.values[active];
                return v == null ? null : (
                  <div key={s.id} className="tt-row">
                    <span className="tt-key" style={{ background: s.color }} />
                    <span className="tt-name">{s.name}</span>
                    <span className="tt-val">{s.format(v)}</span>
                  </div>
                );
              })}
              {extraRows?.(active).map((r) => (
                <div key={r.label} className="tt-row">
                  <span className="tt-key tt-key-blank" />
                  <span className="tt-name">{r.label}</span>
                  <span className="tt-val">{r.value}</span>
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </div>
  );
}
