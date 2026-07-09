import type { RangeKey } from '../lib/types';

export const RANGE_KEYS: RangeKey[] = ['1h', '6h', '24h', '7d', '30d'];

/** Preset pills scoping every history chart below. Live tiles are unaffected. */
export function RangePicker({
  value,
  onChange,
}: {
  value: RangeKey;
  onChange: (r: RangeKey) => void;
}) {
  return (
    <div className="range-picker" role="group" aria-label="History range">
      {RANGE_KEYS.map((k) => (
        <button
          key={k}
          type="button"
          className={k === value ? 'pill selected' : 'pill'}
          aria-pressed={k === value}
          onClick={() => onChange(k)}
        >
          {k}
        </button>
      ))}
    </div>
  );
}
