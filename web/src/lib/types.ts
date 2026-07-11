/**
 * API data types.
 *
 * THIS FILE IS THE CONTRACT — it mirrors internal/metrics/types.go exactly
 * (JSON tags there = property names here). Do not change one without the other.
 */

export interface Snapshot {
  ts: number; // unix milliseconds
  cpu: CPUMetrics;
  mem: MemMetrics;
  disks: DiskMetrics[];
  gpus: GPUMetrics[]; // empty when no GPU is present
}

export interface CPUMetrics {
  usagePct: number; // 0-100, all cores combined
  perCore: number[]; // 0-100 per logical core
  load1: number;
  load5: number;
  load15: number;
}

export interface MemMetrics {
  total: number; // bytes
  used: number;
  available: number;
  usedPct: number;
  swapTotal: number;
  swapUsed: number;
}

export interface DiskMetrics {
  mount: string; // host mount point, e.g. "/"
  device: string;
  fstype: string;
  total: number; // bytes
  used: number;
  free: number;
  usedPct: number;
}

export interface GPUMetrics {
  index: number;
  name: string;
  utilPct: number;
  memTotal: number; // bytes
  memUsed: number;
  tempC: number;
  powerW: number; // 0 when the driver does not report power
}

export interface HostInfo {
  hostname: string;
  os: string; // e.g. "linux", "darwin"
  platform: string; // e.g. "ubuntu 24.04"
  kernelVersion: string;
  arch: string;
  cpuModel: string;
  cpuCores: number; // logical cores
  memTotal: number; // bytes
  bootTime: number; // unix seconds
  hasGPU: boolean;
  gpuNames: string[];
  version: string; // owlwatch version string
}

/** One aggregated time bucket returned by /api/history. */
export interface HistoryPoint {
  ts: number; // bucket start, unix ms
  cpuPct: number; // average over the bucket
  cpuMaxPct: number;
  memUsed: number; // average bytes
  memPct: number;
  swapUsed: number;
  gpuUtilPct?: number; // avg across GPUs; absent when host has no GPU
  gpuMaxPct?: number;
  gpuMemUsed?: number;
  gpuTempC?: number;
  disks: Record<string, number>; // mount -> avg usedPct
}

export type RangeKey = '1h' | '6h' | '24h' | '7d' | '30d';

export interface HistoryResponse {
  range: RangeKey;
  points: HistoryPoint[];
}

/** Payload of the SSE `hello` event sent once per /api/live connection. */
export interface HelloEvent {
  host: HostInfo;
  recent: Snapshot[]; // ring buffer, oldest first — seeds sparklines instantly
  intervalMs: number; // server's sample interval — scales the client stall watchdog
}

/**
 * One monitored server as reported by GET /api/servers (mirrors
 * internal/metrics/federation.go). A standalone instance reports exactly one
 * entry (the local server); a hub reports local + every configured peer.
 */
export interface ServerSummary {
  id: string; // "local" or the configured peer name (slug)
  name: string; // display name: hostname for local, peer name for peers
  local: boolean;
  online: boolean; // local is always true
  lastSeen: number; // unix ms of the last snapshot received; 0 = never
  intervalMs: number; // that server's sample interval; 0 = unknown yet
  host?: HostInfo; // absent until the peer's hello has been seen
  latest?: Snapshot; // most recent snapshot, if any
  /** Last ≤60 CPU usage percentages (oldest first) — seeds overview sparklines. */
  recentCpu?: number[];
}

/** Payload of the `snapshot` event on GET /api/overview/live. */
export interface OverviewSnapshotEvent {
  id: string; // server id
  snapshot: Snapshot;
}

/** Payload of the `status` event on GET /api/overview/live. */
export interface OverviewStatusEvent {
  id: string;
  online: boolean;
  lastSeen: number; // unix ms
}
