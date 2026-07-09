/**
 * Pure chart geometry: SVG path building, tick placement, nearest-point
 * lookup. All functions are side-effect free so the chart component stays
 * declarative.
 */

/** Polyline path; null values lift the pen (gap in the line). */
export function linePath(xs: number[], ys: Array<number | null>): string {
  let d = '';
  let pen = false;
  for (let i = 0; i < xs.length; i++) {
    const y = ys[i];
    if (y == null) {
      pen = false;
      continue;
    }
    d += `${pen ? 'L' : 'M'}${xs[i].toFixed(1)},${y.toFixed(1)}`;
    pen = true;
  }
  return d;
}

/** Area under the line down to baselineY, closed per contiguous run. */
export function areaPath(xs: number[], ys: Array<number | null>, baselineY: number): string {
  let d = '';
  let runStart = -1;
  const base = baselineY.toFixed(1);
  const close = (endIdx: number) => {
    if (runStart < 0) return;
    d += `L${xs[endIdx].toFixed(1)},${base}L${xs[runStart].toFixed(1)},${base}Z`;
    runStart = -1;
  };
  for (let i = 0; i < xs.length; i++) {
    const y = ys[i];
    if (y == null) {
      close(i - 1);
      continue;
    }
    if (runStart < 0) {
      runStart = i;
      d += `M${xs[i].toFixed(1)},${y.toFixed(1)}`;
    } else {
      d += `L${xs[i].toFixed(1)},${y.toFixed(1)}`;
    }
  }
  close(xs.length - 1);
  return d;
}

/** Index of the timestamp nearest to t (timestamps ascending). -1 when empty. */
export function nearestIndex(ts: number[], t: number): number {
  const n = ts.length;
  if (n === 0) return -1;
  if (t <= ts[0]) return 0;
  if (t >= ts[n - 1]) return n - 1;
  let lo = 0;
  let hi = n - 1;
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1;
    if (ts[mid] <= t) lo = mid;
    else hi = mid;
  }
  return t - ts[lo] <= ts[hi] - t ? lo : hi;
}

const MIN = 60_000;
const HOUR = 3_600_000;
const DAY = 86_400_000;
const TICK_STEPS = [
  MIN, 2 * MIN, 5 * MIN, 10 * MIN, 15 * MIN, 30 * MIN,
  HOUR, 2 * HOUR, 4 * HOUR, 6 * HOUR, 12 * HOUR,
  DAY, 2 * DAY, 3 * DAY, 5 * DAY, 7 * DAY, 14 * DAY,
];

/**
 * 4–7 time ticks between t0 and t1, snapped to round local wall-clock
 * boundaries (day steps land on local midnight).
 */
export function pickTimeTicks(t0: number, t1: number, maxTicks = 7): number[] {
  const span = t1 - t0;
  if (span <= 0) return [];
  const step = TICK_STEPS.find((s) => span / s <= maxTicks) ?? TICK_STEPS[TICK_STEPS.length - 1];
  const ticks: number[] = [];
  if (step >= DAY) {
    // Day steps: local calendar arithmetic so each tick lands on local
    // midnight even when the range crosses a DST transition (a fixed UTC
    // offset from t0 would drift to 23:00/01:00 past the boundary).
    const days = step / DAY;
    const d = new Date(t0);
    d.setHours(0, 0, 0, 0);
    if (d.getTime() < t0) d.setDate(d.getDate() + 1);
    while (d.getTime() <= t1) {
      ticks.push(d.getTime());
      d.setDate(d.getDate() + days);
    }
    return ticks;
  }
  // Sub-day steps: align on local wall-clock time (local = utc + off).
  const off = -new Date(t0).getTimezoneOffset() * MIN;
  const first = Math.ceil((t0 + off) / step) * step - off;
  for (let t = first; t <= t1; t += step) ticks.push(t);
  return ticks;
}

/**
 * Nonzero y-ticks for a byte axis with domain [0, max]: power-of-two step,
 * at most 4 divisions. E.g. max = 32 GiB → [8, 16, 24, 32] GiB (in bytes).
 */
export function niceByteTicks(max: number): number[] {
  if (!(max > 0)) return [];
  let step = 1024 ** 2; // 1 MiB
  while (max / step > 4) step *= 2;
  const ticks: number[] = [];
  for (let v = step; v <= max; v += step) ticks.push(v);
  return ticks;
}

/** Evenly sample down to at most `target` points, keeping first and last. */
export function downsample<T>(values: T[], target: number): T[] {
  if (values.length <= target) return values;
  const out: T[] = [];
  for (let i = 0; i < target; i++) {
    out.push(values[Math.round((i * (values.length - 1)) / (target - 1))]);
  }
  return out;
}
