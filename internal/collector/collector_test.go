package collector

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/disk"

	"github.com/CleveroAB/owlwatch/internal/metrics"
)

// newTestCollector builds a collector without touching gopsutil or exec, for
// hermetic tests of the ring buffer, subscription fan-out and disk usage.
func newTestCollector(ringSize int) *Collector {
	return &Collector{
		cfg:          Config{SampleInterval: time.Second, RingSize: ringSize},
		errlog:       newRateLogger(time.Minute),
		usageTimeout: diskUsageTimeout,
		hungMounts:   make(map[string]time.Time),
		ring:         make([]metrics.Snapshot, ringSize),
		subs:         make(map[uint64]chan metrics.Snapshot),
	}
}

func part(device, mount, fstype string) disk.PartitionStat {
	return disk.PartitionStat{Device: device, Mountpoint: mount, Fstype: fstype}
}

func TestFilterPartitions(t *testing.T) {
	tests := []struct {
		name string
		in   []disk.PartitionStat
		want []string // expected mountpoints, in order
	}{
		{
			name: "drops pseudo filesystems",
			in: []disk.PartitionStat{
				part("/dev/sda1", "/", "ext4"),
				part("tmpfs", "/run", "tmpfs"),
				part("proc", "/proc", "proc"),
				part("overlay", "/var/lib/docker/overlay2/abc/merged", "overlay"),
				part("cgroup2", "/sys/fs/cgroup", "cgroup2"),
			},
			want: []string{"/"},
		},
		{
			name: "skips system mount areas but not lookalike siblings",
			in: []disk.PartitionStat{
				part("/dev/sda1", "/", "ext4"),
				part("/dev/sda2", "/boot/efi", "vfat"),
				part("/dev/sda3", "/boot/efi/nested", "vfat"),
				part("/dev/disk3s5", "/System/Volumes/Data", "apfs"),
				part("/dev/disk3s6", "/private/var/vm", "apfs"),
				part("/dev/sdb1", "/boot/efi2", "vfat"), // sibling, not under /boot/efi
			},
			want: []string{"/", "/boot/efi2"},
		},
		{
			name: "requires /dev/ device except for zfs datasets",
			in: []disk.PartitionStat{
				part("rootfs", "/", "ext4"),
				part("tank/data", "/tank/data", "zfs"),
				part("/dev/nvme0n1p2", "/home", "ext4"),
			},
			want: []string{"/home", "/tank/data"},
		},
		{
			name: "dedupes by device keeping the shortest mountpoint",
			in: []disk.PartitionStat{
				part("/dev/sda1", "/mnt/bind-of-root", "ext4"),
				part("/dev/sda1", "/", "ext4"),
				part("/dev/sda1", "/another/bind", "ext4"),
			},
			want: []string{"/"},
		},
		{
			name: "sorts by mountpoint",
			in: []disk.PartitionStat{
				part("/dev/sdc1", "/var", "xfs"),
				part("/dev/sda1", "/", "ext4"),
				part("/dev/sdb1", "/home", "btrfs"),
			},
			want: []string{"/", "/home", "/var"},
		},
		{
			name: "fstype match is case-insensitive",
			in: []disk.PartitionStat{
				part("/dev/disk3s1", "/", "APFS"),
			},
			want: []string{"/"},
		},
		{
			name: "empty input",
			in:   nil,
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterPartitions(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d partitions %v, want %d %v", len(got), mounts(got), len(tt.want), tt.want)
			}
			for i, p := range got {
				if p.Mountpoint != tt.want[i] {
					t.Errorf("partition %d: got mountpoint %q, want %q", i, p.Mountpoint, tt.want[i])
				}
			}
		})
	}
}

func mounts(parts []disk.PartitionStat) []string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = p.Mountpoint
	}
	return out
}

func TestDiskUsagesSkipsHungMounts(t *testing.T) {
	c := newTestCollector(4)
	c.usageTimeout = 20 * time.Millisecond

	block := make(chan struct{})
	defer close(block) // release the abandoned probe goroutine
	var mu sync.Mutex
	calls := map[string]int{}
	c.usageFn = func(_ context.Context, path string) (*disk.UsageStat, error) {
		mu.Lock()
		calls[path]++
		mu.Unlock()
		if path == "/slow" {
			<-block // statfs on a hung mount blocks uninterruptibly
		}
		return &disk.UsageStat{Total: 100, Used: 40, Free: 60, UsedPercent: 40}, nil
	}
	parts := []disk.PartitionStat{
		part("/dev/sda1", "/", "ext4"),
		part("/dev/sdb1", "/slow", "ext4"),
	}
	callCount := func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return calls[path]
	}

	// First tick: the hung mount times out and is dropped; the healthy mount
	// still reports.
	got := c.diskUsages(context.Background(), parts)
	if len(got) != 1 || got[0].Mount != "/" {
		t.Fatalf("diskUsages() = %v, want just /", got)
	}
	if _, hung := c.hungMounts["/slow"]; !hung {
		t.Fatal("/slow not recorded in hungMounts after timeout")
	}

	// Second tick, still inside the backoff window: /slow must be skipped
	// without probing (no goroutine pile-up), / probed again.
	got = c.diskUsages(context.Background(), parts)
	if len(got) != 1 || got[0].Mount != "/" {
		t.Fatalf("diskUsages() = %v, want just /", got)
	}
	if n := callCount("/slow"); n != 1 {
		t.Errorf("usageFn called %d times for /slow, want 1 (backoff skips re-probe)", n)
	}
	if n := callCount("/"); n != 2 {
		t.Errorf("usageFn called %d times for /, want 2", n)
	}

	// Once the backoff expires the mount is re-probed and, now healthy,
	// reported again.
	c.hungMounts["/slow"] = time.Now().Add(-time.Second)
	c.usageFn = func(_ context.Context, _ string) (*disk.UsageStat, error) {
		return &disk.UsageStat{Total: 100, Used: 40, Free: 60, UsedPercent: 40}, nil
	}
	got = c.diskUsages(context.Background(), parts)
	if len(got) != 2 || got[1].Mount != "/slow" {
		t.Fatalf("diskUsages() after backoff = %v, want / and /slow", got)
	}
	if _, hung := c.hungMounts["/slow"]; hung {
		t.Error("/slow still in hungMounts after a successful re-probe")
	}
}

func TestRootfsHostname(t *testing.T) {
	tests := []struct {
		name    string
		content *string // nil = no etc/hostname file
		want    string
	}{
		{"present", strp("hosty-mc-hostface\n"), "hosty-mc-hostface"},
		{"absent", nil, ""},
		{"empty file", strp("\n"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootfs := t.TempDir()
			if tt.content != nil {
				if err := os.MkdirAll(filepath.Join(rootfs, "etc"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(rootfs, "etc/hostname"), []byte(*tt.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := rootfsHostname(rootfs); got != tt.want {
				t.Errorf("rootfsHostname() = %q, want %q", got, tt.want)
			}
		})
	}
}

func strp(s string) *string { return &s }

func TestLatestAndRingBuffer(t *testing.T) {
	c := newTestCollector(4)

	if _, ok := c.Latest(); ok {
		t.Error("Latest() ok = true before first sample, want false")
	}
	if got := c.Recent(); len(got) != 0 {
		t.Errorf("Recent() = %d snapshots before first sample, want 0", len(got))
	}

	for ts := int64(1); ts <= 6; ts++ {
		c.publish(metrics.Snapshot{TS: ts})
	}

	latest, ok := c.Latest()
	if !ok || latest.TS != 6 {
		t.Errorf("Latest() = (TS %d, %v), want (TS 6, true)", latest.TS, ok)
	}
	recent := c.Recent()
	wantTS := []int64{3, 4, 5, 6} // ring of 4, oldest first
	if len(recent) != len(wantTS) {
		t.Fatalf("Recent() returned %d snapshots, want %d", len(recent), len(wantTS))
	}
	for i, snap := range recent {
		if snap.TS != wantTS[i] {
			t.Errorf("Recent()[%d].TS = %d, want %d", i, snap.TS, wantTS[i])
		}
	}
}

func TestSubscribeDropsWithoutBlocking(t *testing.T) {
	c := newTestCollector(4)
	ch, cancel := c.Subscribe()
	defer cancel()

	// Publish far more than the subscriber buffer without reading; publish
	// must never block.
	total := subscriberBuffer + 10
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ts := 1; ts <= total; ts++ {
			c.publish(metrics.Snapshot{TS: int64(ts)})
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish blocked on a slow subscriber")
	}

	// The subscriber sees exactly the first subscriberBuffer snapshots; the
	// rest were dropped.
	for want := int64(1); want <= subscriberBuffer; want++ {
		snap := <-ch
		if snap.TS != want {
			t.Fatalf("received TS %d, want %d", snap.TS, want)
		}
	}
	select {
	case snap := <-ch:
		t.Fatalf("received extra snapshot TS %d, want none", snap.TS)
	default:
	}
}

func TestSubscribeCancelIsIdempotent(t *testing.T) {
	c := newTestCollector(4)
	ch, cancel := c.Subscribe()

	cancel()
	cancel() // second call must be a no-op, not a double-close panic

	if _, ok := <-ch; ok {
		t.Error("channel still open after cancel")
	}
	c.publish(metrics.Snapshot{TS: 1}) // must not panic on the removed sub
}

func TestSubscribeAfterShutdown(t *testing.T) {
	c := newTestCollector(4)
	ch1, cancel1 := c.Subscribe()

	c.shutdown()

	if _, ok := <-ch1; ok {
		t.Error("existing subscriber channel still open after shutdown")
	}
	cancel1() // must not panic on the already-closed channel

	ch2, cancel2 := c.Subscribe()
	if _, ok := <-ch2; ok {
		t.Error("Subscribe after shutdown returned an open channel")
	}
	cancel2()
}

func TestNewDefaults(t *testing.T) {
	c := New(Config{})
	if c.cfg.SampleInterval != defaultSampleInterval {
		t.Errorf("SampleInterval = %v, want %v", c.cfg.SampleInterval, defaultSampleInterval)
	}
	if c.cfg.RingSize != defaultRingSize {
		t.Errorf("RingSize = %d, want %d", c.cfg.RingSize, defaultRingSize)
	}
	info := c.HostInfo()
	if info.Version != "" {
		t.Errorf("HostInfo().Version = %q, want empty (main.go fills it)", info.Version)
	}
	if info.GPUNames == nil {
		t.Error("HostInfo().GPUNames is nil, want non-nil for JSON encoding")
	}
	if info.CPUCores <= 0 {
		t.Errorf("HostInfo().CPUCores = %d, want > 0", info.CPUCores)
	}
	if _, ok := c.Latest(); ok {
		t.Error("Latest() ok = true before Run, want false")
	}
}

func TestRunSamplesAndShutsDown(t *testing.T) {
	c := New(Config{SampleInterval: 50 * time.Millisecond, RingSize: 8})
	ch, cancelSub := c.Subscribe()
	defer cancelSub()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()

	select {
	case snap := <-ch:
		if snap.TS == 0 {
			t.Error("snapshot has zero timestamp")
		}
		if snap.CPU.PerCore == nil || snap.Disks == nil || snap.GPUs == nil {
			t.Error("snapshot slices must be non-nil so JSON encodes [] not null")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no snapshot received within 3s")
	}
	if _, ok := c.Latest(); !ok {
		t.Error("Latest() ok = false after sampling started")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return on context cancel")
	}

	// After Run returns, subscriber channels drain and then close.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("subscriber channel not closed after Run returned")
		}
	}
}
