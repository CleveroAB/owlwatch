import { describe, expect, it } from 'vitest';
import type { CPUMetrics, DiskMetrics, MemMetrics, ProcessCPUMetrics, ProcessMemoryMetrics } from '../lib/types';
import { containingMount, largestDisks, parentDiskPath, topCpuProcesses, topMemoryProcesses } from './StatTiles';

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

function cpu(topProcesses?: ProcessCPUMetrics[]): CPUMetrics {
  return { usagePct: 50, perCore: [50], load1: 1, load5: 1, load15: 1, topProcesses };
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
    expect(topCpuProcesses(cpu())).toEqual([]);
  });

  it('sorts CPU processes by share, ties by PID, and caps the result', () => {
    const rows = Array.from({ length: 12 }, (_, i) => ({ pid: 12 - i, name: `p${i}`, usagePct: Math.floor(i / 2) }));
    const got = topCpuProcesses(cpu(rows));
    expect(got).toHaveLength(10);
    expect(got.map((row) => row.usagePct)).toEqual([5, 5, 4, 4, 3, 3, 2, 2, 1, 1]);
    expect(got[0].pid).toBeLessThan(got[1].pid);
  });

  it('ranks disks by used bytes rather than fullness percentage', () => {
    const got = largestDisks([disk('/small-full', 90), disk('/large', 950, 1000), disk('/tiny', 10)], 2);
    expect(got.map((row) => row.mount)).toEqual(['/large', '/small-full']);
  });

  it('selects the most specific mount for a drill-down path', () => {
    const disks = [disk('/', 50), disk('/boot', 20), disk('/data/archive', 10)];
    expect(containingMount('/data/archive/2026/file.zip', disks)?.mount).toBe('/data/archive');
    expect(containingMount('/boot/grub', disks)?.mount).toBe('/boot');
  });

  it('navigates up without escaping the selected mount', () => {
    expect(parentDiskPath('/var/lib/docker', '/')).toBe('/var/lib');
    expect(parentDiskPath('/data/projects', '/data')).toBe('/data');
    expect(parentDiskPath('/data', '/data')).toBe('/data');
  });
});
