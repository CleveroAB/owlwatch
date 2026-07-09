import { useMemo } from 'react';
import { downsample, linePath } from '../lib/geometry';
import { useSize } from '../hooks/useSize';

const HEIGHT = 36;
const PAD = 4;
const POINTS = 60;

/**
 * Bare stat-tile trend: 2px line in the series hue, fixed 0–100 domain, last
 * point marked with a small dot. No axes, no fill, no tooltip.
 */
export function Sparkline({ values, color }: { values: Array<number | null>; color: string }) {
  const { ref, width } = useSize<HTMLDivElement>();
  const vals = useMemo(() => downsample(values, POINTS), [values]);

  const n = vals.length;
  const drawable = width > 20 && n >= 2;

  let path = '';
  let last: { x: number; y: number } | null = null;
  if (drawable) {
    const xFor = (i: number) => 2 + (i * (width - 6)) / (n - 1);
    const yFor = (v: number) => PAD + (1 - Math.min(100, Math.max(0, v)) / 100) * (HEIGHT - 2 * PAD);
    const xs = vals.map((_, i) => xFor(i));
    const ys = vals.map((v) => (v == null ? null : yFor(v)));
    path = linePath(xs, ys);
    for (let i = n - 1; i >= 0; i--) {
      const v = vals[i];
      if (v != null) {
        last = { x: xFor(i), y: yFor(v) };
        break;
      }
    }
  }

  return (
    <div ref={ref} className="spark" aria-hidden="true">
      {drawable && path && (
        <svg width={width} height={HEIGHT}>
          <path
            d={path}
            fill="none"
            stroke={color}
            strokeWidth={2}
            strokeLinejoin="round"
            strokeLinecap="round"
          />
          {last && <circle cx={last.x} cy={last.y} r={2} fill={color} />}
        </svg>
      )}
    </div>
  );
}
