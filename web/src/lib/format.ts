/**
 * Pure formatting helpers. No state, no locale surprises — axis labels use
 * fixed English abbreviations so ticks are deterministic everywhere.
 */

import type { RangeKey } from './types';

const UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
const GIB = 1024 ** 3;

/** Binary units, one decimal: 13314398618 → "12.4 GiB". */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B';
  let v = n;
  let i = 0;
  while (v >= 1024 && i < UNITS.length - 1) {
    v /= 1024;
    i++;
  }
  return i === 0 ? `${Math.round(v)} B` : `${v.toFixed(1)} ${UNITS[i]}`;
}

/** Like formatBytes but drops a trailing ".0" — for axis ticks and totals. */
export function formatBytesTick(n: number): string {
  if (n === 0) return '0';
  return formatBytes(n).replace('.0 ', ' ');
}

export function formatPct(v: number): string {
  return `${v.toFixed(1)}%`;
}

/** "4.1 / 24 GiB" — both values in GiB, decimals only when needed. */
export function formatGiBPair(usedBytes: number, totalBytes: number): string {
  const fmt = (bytes: number) => {
    const v = Math.round((bytes / GIB) * 10) / 10;
    return Number.isInteger(v) ? String(v) : v.toFixed(1);
  };
  return `${fmt(usedBytes)} / ${fmt(totalBytes)} GiB`;
}

/** "12d 4h 07m"; shorter forms below a day / an hour. */
export function formatUptime(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}d ${h}h ${pad2(m)}m`;
  if (h > 0) return `${h}h ${pad2(m)}m`;
  return `${m}m ${pad2(s % 60)}s`;
}

/**
 * Middle-truncate a mount path, keeping the first segment and the (distinctive)
 * tail: "/Library/Developer/CoreSimulator/Volumes/iOS_23C54" → "/Library/…/iOS_23C54".
 * End-truncation is useless here — sibling mounts share long prefixes.
 */
export function truncateMountPath(path: string, max = 28): string {
  if (path.length <= max) return path;
  const parts = path.split('/').filter(Boolean);
  const tail = parts[parts.length - 1] ?? path;
  if (parts.length >= 2) {
    const short = `/${parts[0]}/…/${tail}`;
    if (short.length <= max) return short;
  }
  return `…${tail.slice(-(max - 1))}`;
}

/**
 * Relative "ago" label for overview cards: "just now", "4m ago",
 * "3h 12m ago", "5d ago". `ts` is unix ms.
 */
export function formatAgo(ts: number, now: number = Date.now()): string {
  const s = Math.max(0, Math.floor((now - ts) / 1000));
  if (s < 60) return 'just now';
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${pad2(m % 60)}m ago`;
  return `${Math.floor(h / 24)}d ago`;
}

/** Wall-clock "14:32" for the offline-peer "last data" notice. `ts` is unix ms. */
export function formatClock(ts: number): string {
  return hm(new Date(ts));
}

const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

function pad2(n: number): string {
  return String(n).padStart(2, '0');
}

function hm(d: Date): string {
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

/** X-axis tick label, by range: 1h/6h → "14:05", 24h → "Tue 14:00", 7d/30d → "Jun 12". */
export function formatAxisTick(ts: number, range: RangeKey): string {
  const d = new Date(ts);
  switch (range) {
    case '1h':
    case '6h':
      return hm(d);
    case '24h':
      return `${DAYS[d.getDay()]} ${hm(d)}`;
    default:
      return `${MONTHS[d.getMonth()]} ${d.getDate()}`;
  }
}

/** Timestamp for tooltips and table rows — more precise than axis ticks. */
export function formatPointTime(ts: number, range: RangeKey): string {
  const d = new Date(ts);
  switch (range) {
    case '1h':
      return `${hm(d)}:${pad2(d.getSeconds())}`;
    case '6h':
      return hm(d);
    case '24h':
      return `${DAYS[d.getDay()]} ${hm(d)}`;
    default:
      return `${MONTHS[d.getMonth()]} ${d.getDate()}, ${hm(d)}`;
  }
}
