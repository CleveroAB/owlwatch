/**
 * 4px capacity meter. The fill wears the series hue until the value enters a
 * status band (≥80% warn, ≥92% critical); the track is the hue at ~18%.
 * The numeric value is always present in adjacent text, so this is aria-hidden.
 */
export function Meter({ pct, hue }: { pct: number; hue: string }) {
  const clamped = Math.min(100, Math.max(0, pct));
  const fill =
    clamped >= 92 ? 'var(--status-critical)' : clamped >= 80 ? 'var(--status-warn)' : hue;
  return (
    <div
      className="meter"
      aria-hidden="true"
      style={{ background: `color-mix(in srgb, ${hue} 18%, transparent)` }}
    >
      <div className="meter-fill" style={{ width: `${clamped}%`, background: fill }} />
    </div>
  );
}

export type MeterFlag = 'warn' | 'critical' | null;

/** Status band for a 0–100 value — drives both the meter color and the tile flag. */
export function meterFlag(pct: number): MeterFlag {
  if (pct >= 92) return 'critical';
  if (pct >= 80) return 'warn';
  return null;
}
