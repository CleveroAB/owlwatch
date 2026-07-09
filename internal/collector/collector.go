// Package collector samples host metrics (CPU, memory, disk, GPU) on a fixed
// interval, keeps a short in-memory ring buffer of recent snapshots, and
// broadcasts each snapshot to subscribers without ever blocking the sampler.
package collector

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/CleveroAB/owlwatch/internal/metrics"
)

const (
	defaultSampleInterval = 2 * time.Second
	defaultRingSize       = 150 // 5 min at 2s
	startupTimeout        = 10 * time.Second

	// subscriberBuffer is the per-subscriber channel capacity. A subscriber
	// that falls further behind than this loses snapshots (see publish).
	subscriberBuffer = 16

	// diskUsageTimeout bounds how long one mount's statfs may take before the
	// sampler gives up on it for this tick (see Collector.usage).
	diskUsageTimeout = 2 * time.Second
	// diskReprobeInterval is how long a timed-out mount stays skipped before
	// the sampler tries it again — same backoff pattern as nvidia-smi.
	diskReprobeInterval = time.Minute
)

// Config configures a Collector. Zero values fall back to the documented
// defaults.
type Config struct {
	SampleInterval time.Duration // default 2s
	Rootfs         string        // OWLWATCH_ROOTFS; "" = native mode
	RingSize       int           // default 150 (5 min at 2s)
}

// Collector samples metrics on a ticker (see Run) and fans the resulting
// snapshots out to subscribers.
type Collector struct {
	cfg    Config
	gpu    *gpuPoller
	errlog *rateLogger
	host   metrics.HostInfo // gathered once in New, immutable afterwards

	// usageFn and usageTimeout are disk.UsageWithContext and diskUsageTimeout
	// in production; tests inject a fake fn and a short timeout.
	usageFn      func(ctx context.Context, path string) (*disk.UsageStat, error)
	usageTimeout time.Duration
	// hungMounts maps a mountpoint whose statfs timed out to the earliest
	// time it may be probed again. Only touched from the Run goroutine.
	hungMounts map[string]time.Time

	mu      sync.Mutex
	ring    []metrics.Snapshot // circular buffer of the most recent snapshots
	head    int                // next write position in ring
	count   int                // number of valid entries in ring
	latest  metrics.Snapshot
	sampled bool // true once the first sample has been published
	subs    map[uint64]chan metrics.Snapshot
	nextSub uint64
	stopped bool // Run has returned; all subscriber channels are closed
}

// New builds a Collector: it primes the CPU usage counters (cpu.Percent with
// interval 0 reports the delta since the previous call), probes once for an
// NVIDIA GPU, and caches HostInfo. Call Run to start sampling.
func New(cfg Config) *Collector {
	if cfg.SampleInterval <= 0 {
		cfg.SampleInterval = defaultSampleInterval
	}
	if cfg.RingSize <= 0 {
		cfg.RingSize = defaultRingSize
	}
	c := &Collector{
		cfg:          cfg,
		errlog:       newRateLogger(time.Minute),
		usageFn:      disk.UsageWithContext,
		usageTimeout: diskUsageTimeout,
		hungMounts:   make(map[string]time.Time),
		ring:         make([]metrics.Snapshot, cfg.RingSize),
		subs:         make(map[uint64]chan metrics.Snapshot),
	}
	c.gpu = newGPUPoller(c.errlog)

	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()

	if _, err := cpu.PercentWithContext(ctx, 0, false); err != nil {
		c.errlog.printf("cpu", "collector: priming cpu counters: %v", err)
	}
	if _, err := cpu.PercentWithContext(ctx, 0, true); err != nil {
		c.errlog.printf("cpu-core", "collector: priming per-core cpu counters: %v", err)
	}

	c.host = c.gatherHostInfo(ctx)
	return c
}

// Run blocks, sampling on a ticker until ctx is cancelled. When it returns,
// every subscriber channel has been closed.
func (c *Collector) Run(ctx context.Context) {
	defer c.shutdown()

	c.sample(ctx) // sample immediately so Latest/healthz flip without delay
	ticker := time.NewTicker(c.cfg.SampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.sample(ctx)
		}
	}
}

// Subscribe returns a channel of future snapshots plus a cancel func. The
// channel is buffered; a subscriber that falls behind loses snapshots rather
// than blocking the sampler. The channel is closed by the (idempotent) cancel
// func, or when Run exits.
func (c *Collector) Subscribe() (<-chan metrics.Snapshot, func()) {
	ch := make(chan metrics.Snapshot, subscriberBuffer)

	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	id := c.nextSub
	c.nextSub++
	c.subs[id] = ch
	c.mu.Unlock()

	cancel := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if sub, ok := c.subs[id]; ok {
			delete(c.subs, id)
			close(sub)
		}
	}
	return ch, cancel
}

// Latest returns the most recent snapshot; ok is false before the first
// sample has been taken.
func (c *Collector) Latest() (metrics.Snapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latest, c.sampled
}

// Recent returns a copy of the ring buffer, oldest first.
func (c *Collector) Recent() []metrics.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]metrics.Snapshot, 0, c.count)
	start := c.head - c.count
	if start < 0 {
		start += len(c.ring)
	}
	for i := 0; i < c.count; i++ {
		out = append(out, c.ring[(start+i)%len(c.ring)])
	}
	return out
}

// HostInfo returns the host identity cached at startup. Version is
// deliberately left empty: main.go stamps its build version onto the returned
// struct before handing it to the HTTP server.
func (c *Collector) HostInfo() metrics.HostInfo {
	return c.host
}

// shutdown marks the collector stopped and closes all subscriber channels.
func (c *Collector) shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	for id, ch := range c.subs {
		delete(c.subs, id)
		close(ch)
	}
}

// sample takes one reading of every metric and publishes it. Each section is
// independent: a failing probe logs (rate-limited) and zero-values its
// section, but never skips the tick.
func (c *Collector) sample(ctx context.Context) {
	c.publish(metrics.Snapshot{
		TS:    time.Now().UnixMilli(),
		CPU:   c.sampleCPU(ctx),
		Mem:   c.sampleMem(ctx),
		Disks: c.sampleDisks(ctx),
		GPUs:  c.gpu.sample(ctx),
	})
}

// publish records a snapshot and fans it out to subscribers. Sends are
// non-blocking: a subscriber with a full buffer loses this snapshot instead
// of stalling the sampler.
func (c *Collector) publish(snap metrics.Snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.latest = snap
	c.sampled = true
	c.ring[c.head] = snap
	c.head = (c.head + 1) % len(c.ring)
	if c.count < len(c.ring) {
		c.count++
	}
	for _, ch := range c.subs {
		select {
		case ch <- snap:
		default:
		}
	}
}

func (c *Collector) sampleCPU(ctx context.Context) metrics.CPUMetrics {
	m := metrics.CPUMetrics{PerCore: []float64{}}
	// Interval 0 = delta since the previous call (primed in New). A non-zero
	// interval would block the sampler for that long — never use one.
	if pct, err := cpu.PercentWithContext(ctx, 0, false); err != nil {
		c.errlog.printf("cpu", "collector: cpu usage: %v", err)
	} else if len(pct) > 0 {
		m.UsagePct = pct[0]
	}
	if per, err := cpu.PercentWithContext(ctx, 0, true); err != nil {
		c.errlog.printf("cpu-core", "collector: per-core cpu usage: %v", err)
	} else if len(per) > 0 {
		m.PerCore = per
	}
	if avg, err := load.AvgWithContext(ctx); err != nil {
		c.errlog.printf("load", "collector: load average: %v", err)
	} else {
		m.Load1, m.Load5, m.Load15 = avg.Load1, avg.Load5, avg.Load15
	}
	return m
}

func (c *Collector) sampleMem(ctx context.Context) metrics.MemMetrics {
	var m metrics.MemMetrics
	if vm, err := mem.VirtualMemoryWithContext(ctx); err != nil {
		c.errlog.printf("mem", "collector: virtual memory: %v", err)
	} else {
		m.Total = vm.Total
		m.Used = vm.Used
		m.Available = vm.Available
		m.UsedPct = vm.UsedPercent
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err != nil {
		c.errlog.printf("swap", "collector: swap memory: %v", err)
	} else {
		m.SwapTotal = sw.Total
		m.SwapUsed = sw.Used
	}
	return m
}

// sampleDisks enumerates partitions, filters them to real filesystems and
// reads usage for each. In container mode (cfg.Rootfs set) the partition list
// already comes from the host's mount table via HOST_PROC; usage is then
// statfs'd through the host root bind-mounted at Rootfs, while the reported
// mountpoint stays the host's own.
func (c *Collector) sampleDisks(ctx context.Context) []metrics.DiskMetrics {
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		c.errlog.printf("disk", "collector: listing partitions: %v", err)
		return []metrics.DiskMetrics{}
	}
	return c.diskUsages(ctx, filterPartitions(parts))
}

// diskUsages reads usage for each (already filtered) partition. A hung mount
// (dead FUSE daemon, wedged network block device) can block statfs
// uninterruptibly and must not wedge the sampler: each read is bounded by
// usageTimeout (see usage), and a mount that times out lands in hungMounts
// and is skipped until diskReprobeInterval has passed — same backoff pattern
// as nvidia-smi — so at most one probe goroutine per mount can be outstanding.
func (c *Collector) diskUsages(ctx context.Context, parts []disk.PartitionStat) []metrics.DiskMetrics {
	out := []metrics.DiskMetrics{}
	now := time.Now()
	for _, p := range parts {
		if next, hung := c.hungMounts[p.Mountpoint]; hung {
			if now.Before(next) {
				continue
			}
			delete(c.hungMounts, p.Mountpoint)
		}
		path := p.Mountpoint
		if c.cfg.Rootfs != "" {
			path = filepath.Join(c.cfg.Rootfs, p.Mountpoint)
		}
		u, err := c.usage(ctx, path)
		if errors.Is(err, errUsageTimeout) {
			c.hungMounts[p.Mountpoint] = now.Add(diskReprobeInterval)
			c.errlog.printf("disk:"+p.Mountpoint, "collector: disk usage for %s timed out (mount hung? re-probing in %s)", path, diskReprobeInterval)
			continue
		}
		if err != nil {
			// In container mode a host mount may not be visible through the
			// bind-mounted root — skip it.
			c.errlog.printf("disk:"+p.Mountpoint, "collector: disk usage for %s: %v", path, err)
			continue
		}
		if u.Total == 0 {
			continue
		}
		out = append(out, metrics.DiskMetrics{
			Mount:   p.Mountpoint,
			Device:  p.Device,
			Fstype:  p.Fstype,
			Total:   u.Total,
			Used:    u.Used,
			Free:    u.Free,
			UsedPct: u.UsedPercent,
		})
	}
	return out
}

// errUsageTimeout is returned by usage when a mount's statfs did not finish
// within usageTimeout.
var errUsageTimeout = errors.New("disk usage timed out")

// usage calls usageFn in a goroutine and abandons it after usageTimeout.
// gopsutil's disk.UsageWithContext ignores its context (it calls statfs
// directly), and statfs on a hung mount blocks uninterruptibly, so waiting is
// not an option: on timeout the goroutine is left to leak until the syscall
// eventually returns. hungMounts caps the leak at one goroutine per hung
// mount per re-probe.
func (c *Collector) usage(ctx context.Context, path string) (*disk.UsageStat, error) {
	type result struct {
		u   *disk.UsageStat
		err error
	}
	fn := c.usageFn
	ch := make(chan result, 1) // buffered so an abandoned goroutine can exit
	go func() {
		u, err := fn(ctx, path)
		ch <- result{u, err}
	}()
	select {
	case r := <-ch:
		return r.u, r.err
	case <-time.After(c.usageTimeout):
		return nil, errUsageTimeout
	}
}

// gatherHostInfo collects the static host identity once at startup. Version
// is left empty on purpose — see HostInfo.
func (c *Collector) gatherHostInfo(ctx context.Context) metrics.HostInfo {
	info := metrics.HostInfo{
		Arch:     runtime.GOARCH,
		CPUCores: runtime.NumCPU(),
		GPUNames: []string{}, // non-nil so JSON encodes [] rather than null
	}
	if hi, err := host.InfoWithContext(ctx); err != nil {
		c.errlog.printf("hostinfo", "collector: host info: %v", err)
	} else {
		info.Hostname = hi.Hostname
		info.OS = hi.OS
		info.Platform = strings.TrimSpace(hi.Platform + " " + hi.PlatformVersion)
		info.KernelVersion = hi.KernelVersion
		info.BootTime = int64(hi.BootTime)
	}
	if c.cfg.Rootfs != "" {
		// host.Info reads os.Hostname(), which inside a container reports the
		// container's UTS hostname (typically the container ID) — HOST_PROC/
		// HOST_ETC don't redirect it. Prefer the host's own /etc/hostname via
		// the bind-mounted root, keeping the gopsutil value as fallback.
		if hn := rootfsHostname(c.cfg.Rootfs); hn != "" {
			info.Hostname = hn
		}
	}
	if cis, err := cpu.InfoWithContext(ctx); err != nil {
		c.errlog.printf("cpuinfo", "collector: cpu info: %v", err)
	} else if len(cis) > 0 {
		info.CPUModel = cis[0].ModelName
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err != nil {
		c.errlog.printf("mem", "collector: virtual memory: %v", err)
	} else {
		info.MemTotal = vm.Total
	}
	for _, g := range c.gpu.probeInitial(ctx) {
		info.HasGPU = true
		info.GPUNames = append(info.GPUNames, g.Name)
	}
	return info
}

// rootfsHostname reads the host's /etc/hostname through the root bind-mounted
// at rootfs. It returns "" when the file is missing, unreadable, or blank —
// the caller then keeps the gopsutil hostname.
func rootfsHostname(rootfs string) string {
	b, err := os.ReadFile(filepath.Join(rootfs, "etc/hostname"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// allowedFstypes is the allowlist of real (user-relevant) filesystems;
// everything else — tmpfs, overlay, proc, cgroup, ... — is noise.
var allowedFstypes = map[string]struct{}{
	"ext4": {}, "ext3": {}, "ext2": {}, "xfs": {}, "btrfs": {}, "zfs": {},
	"apfs": {}, "hfs": {}, "hfsplus": {}, "ntfs": {}, "fuseblk": {},
	"vfat": {}, "exfat": {}, "f2fs": {},
}

// skippedMountPrefixes are mount areas that pass the fstype allowlist but are
// system plumbing, not storage the user cares about.
var skippedMountPrefixes = []string{"/boot/efi", "/System", "/private/var/vm"}

// filterPartitions keeps real filesystems: allowlisted fstypes on /dev/*
// devices (zfs datasets are exempt from the /dev/ rule), outside the skipped
// mount areas, deduplicated by device (shortest mountpoint wins) and sorted
// by mountpoint.
func filterPartitions(parts []disk.PartitionStat) []disk.PartitionStat {
	byDevice := make(map[string]disk.PartitionStat)
	for _, p := range parts {
		fstype := strings.ToLower(p.Fstype)
		if _, ok := allowedFstypes[fstype]; !ok {
			continue
		}
		if !strings.HasPrefix(p.Device, "/dev/") && fstype != "zfs" {
			continue
		}
		if underAny(p.Mountpoint, skippedMountPrefixes) {
			continue
		}
		cur, seen := byDevice[p.Device]
		if !seen || betterMount(p.Mountpoint, cur.Mountpoint) {
			byDevice[p.Device] = p
		}
	}
	out := make([]disk.PartitionStat, 0, len(byDevice))
	for _, p := range byDevice {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mountpoint < out[j].Mountpoint })
	return out
}

// betterMount reports whether mountpoint a should replace b as the
// representative for a device: shorter wins, ties break lexicographically.
func betterMount(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// underAny reports whether mount equals or lives under any of the prefixes.
func underAny(mount string, prefixes []string) bool {
	for _, pre := range prefixes {
		if mount == pre || strings.HasPrefix(mount, pre+"/") {
			return true
		}
	}
	return false
}

// rateLogger logs a given message key at most once per interval, so a probe
// that fails on every 2s tick doesn't flood the log.
type rateLogger struct {
	every time.Duration

	mu   sync.Mutex
	last map[string]time.Time
}

func newRateLogger(every time.Duration) *rateLogger {
	return &rateLogger{every: every, last: make(map[string]time.Time)}
}

func (l *rateLogger) printf(key, format string, args ...any) {
	l.mu.Lock()
	now := time.Now()
	if t, ok := l.last[key]; ok && now.Sub(t) < l.every {
		l.mu.Unlock()
		return
	}
	l.last[key] = now
	l.mu.Unlock()
	log.Printf(format, args...)
}
