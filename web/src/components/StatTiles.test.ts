import { describe, expect, it } from 'vitest';
import type { DiskMetrics, MemMetrics, ProcessMemoryMetrics } from '../lib/types';
import { largestDisks, topMemoryProcesses } from './StatTiles';

function process(pid: number, used: number): ProcessMemoryMetrics {
  return { pid, name: `process-${pid}`, used, usedPct: used / 100 };
}

function memory(topProcesses?: ProcessMemoryMetrics[]): MemMetrics {
  return {
    total: 100,
    used: 50,
    available: 50,
    usedPct: 50,
    swapTotal: 0,
    swapUsed: 0,
    topProcesses,
  };
}

function disk(mount: string, used: number, total = 100): DiskMetrics {
  return { mount, device: mount, fstype: 'ext4', total, used, free: total - used, usedPct: used / total * 100 };
}

describe('expanded resource rankings', () => {
  it('sorts memory processes by resident bytes and caps the result', () => {
    const rows = Array.from({ length: 12 }, (_, i) => process(12 - i, i));
    const got = topMemoryProcesses(memory(rows));
    expect(got).toHaveLength(10);
    expect(got.map((row) => row.used)).toEqual([11, 10, 9, 8, 7, 6, 5, 4, 3, 2]);
  });

  it('handles snapshots from older peers without process details', () => {
    expect(topMemoryProcesses(memory())).toEqual([]);
  });

  it('ranks disks by used bytes rather than fullness percentage', () => {
    const got = largestDisks([disk('/small-full', 90), disk('/large', 950, 1000), disk('/tiny', 10)], 2);
    expect(got.map((row) => row.mount)).toEqual(['/large', '/small-full']);
  });
});
