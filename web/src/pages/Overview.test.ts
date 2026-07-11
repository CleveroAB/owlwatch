import { describe, expect, it } from 'vitest';
import type { ServerSummary, Snapshot } from '../lib/types';
import { mergeSparks } from './Overview';

function summary(id: string, extra: Partial<ServerSummary> = {}): ServerSummary {
  return { id, name: id, local: id === 'local', online: true, lastSeen: 0, intervalMs: 2000, ...extra };
}

function snapshotWithCpu(usagePct: number): Snapshot {
  return {
    ts: 1,
    cpu: { usagePct, perCore: [usagePct], load1: 0, load5: 0, load15: 0 },
    mem: { total: 1, used: 0, available: 1, usedPct: 0, swapTotal: 0, swapUsed: 0 },
    disks: [],
    gpus: [],
  };
}

describe('mergeSparks', () => {
  it('seeds an unseen server from recentCpu (first paint, §9.5)', () => {
    const next = mergeSparks({}, [summary('web1', { recentCpu: [1, 2, 3] })]);
    expect(next['web1']).toEqual([1, 2, 3]);
  });

  it('caps a recentCpu seed to the sparkline length, keeping the newest points', () => {
    const long = Array.from({ length: 90 }, (_, i) => i);
    const next = mergeSparks({}, [summary('web1', { recentCpu: long })]);
    expect(next['web1']).toHaveLength(60);
    expect(next['web1']![0]).toBe(30); // oldest kept
    expect(next['web1']![59]).toBe(89); // newest kept
  });

  it('never shrinks an accumulated ring on a servers resync', () => {
    const accumulated = Array.from({ length: 40 }, (_, i) => i);
    const next = mergeSparks(
      { web1: accumulated },
      [summary('web1', { recentCpu: [7, 8] })],
    );
    expect(next['web1']).toBe(accumulated);
  });

  it('prefers the accumulated ring over an equally long seed', () => {
    const accumulated = [1, 2, 3];
    const next = mergeSparks({ web1: accumulated }, [summary('web1', { recentCpu: [4, 5, 6] })]);
    expect(next['web1']).toBe(accumulated);
  });

  it('replaces a shorter accumulated ring with a longer recentCpu seed', () => {
    const next = mergeSparks({ web1: [9] }, [summary('web1', { recentCpu: [1, 2, 3, 4] })]);
    expect(next['web1']).toEqual([1, 2, 3, 4]);
  });

  it('falls back to a single point from latest when recentCpu is absent', () => {
    const next = mergeSparks({}, [summary('web1', { latest: snapshotWithCpu(42) })]);
    expect(next['web1']).toEqual([42]);
  });

  it('falls back to empty when there is no data at all', () => {
    const next = mergeSparks({}, [summary('web1')]);
    expect(next['web1']).toEqual([]);
  });

  it('drops rings for servers no longer in the fleet', () => {
    const next = mergeSparks({ gone: [1, 2] }, [summary('web1', { recentCpu: [5] })]);
    expect(next).toEqual({ web1: [5] });
  });
});
