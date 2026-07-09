package store

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/CleveroAB/owlwatch/internal/metrics"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "owlwatch.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// snap builds a GPU-less snapshot with two disk mounts.
func snap(ts time.Time, cpu float64, memUsed uint64, rootPct float64) metrics.Snapshot {
	return metrics.Snapshot{
		TS:  ts.UnixMilli(),
		CPU: metrics.CPUMetrics{UsagePct: cpu},
		Mem: metrics.MemMetrics{Total: 32 << 30, Used: memUsed, UsedPct: 40, SwapUsed: 100},
		Disks: []metrics.DiskMetrics{
			{Mount: "/", Device: "/dev/sda1", Fstype: "ext4", UsedPct: rootPct},
			{Mount: "/data", Device: "/dev/sdb1", Fstype: "ext4", UsedPct: 10},
		},
	}
}

func mustInsert(t *testing.T, s *Store, snaps ...metrics.Snapshot) {
	t.Helper()
	for _, sn := range snaps {
		if err := s.Insert(sn); err != nil {
			t.Fatalf("Insert(ts=%d): %v", sn.TS, err)
		}
	}
}

func mustQuery(t *testing.T, s *Store, r Range, now time.Time) []metrics.HistoryPoint {
	t.Helper()
	pts, err := s.Query(r, now)
	if err != nil {
		t.Fatalf("Query(%q): %v", r.Key, err)
	}
	return pts
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestOpenCreatesParentDirsAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "owlwatch.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open with missing parent dirs: %v", err)
	}
	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	mustInsert(t, s, snap(base, 50, 1000, 70))
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen: migration must be idempotent and data must survive.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	pts := mustQuery(t, s2, Ranges["1h"], base.Add(time.Minute))
	if len(pts) != 1 {
		t.Fatalf("after reopen got %d points, want 1", len(pts))
	}
}

// TestPragmasApplied guards the modernc DSN syntax: a malformed _pragma
// parameter fails silently for journal_mode, leaving the default (delete).
func TestPragmasApplied(t *testing.T) {
	s := newTestStore(t)

	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}

	var timeout int
	if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if timeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", timeout)
	}

	var sync int
	if err := s.db.QueryRow(`PRAGMA synchronous`).Scan(&sync); err != nil {
		t.Fatalf("read synchronous: %v", err)
	}
	if sync != 1 { // 1 = NORMAL
		t.Errorf("synchronous = %d, want 1 (NORMAL)", sync)
	}
}

func TestRanges(t *testing.T) {
	want := map[string]Range{
		"1h":  {Key: "1h", Dur: time.Hour, Bucket: 10 * time.Second},
		"6h":  {Key: "6h", Dur: 6 * time.Hour, Bucket: time.Minute},
		"24h": {Key: "24h", Dur: 24 * time.Hour, Bucket: 5 * time.Minute},
		"7d":  {Key: "7d", Dur: 7 * 24 * time.Hour, Bucket: 30 * time.Minute},
		"30d": {Key: "30d", Dur: 30 * 24 * time.Hour, Bucket: 2 * time.Hour},
	}
	if len(Ranges) != len(want) {
		t.Fatalf("Ranges has %d entries, want %d", len(Ranges), len(want))
	}
	for k, w := range want {
		if got, ok := Ranges[k]; !ok || got != w {
			t.Errorf("Ranges[%q] = %+v, want %+v", k, got, w)
		}
	}
}

func TestQueryBucketing(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)

	// 12 samples at 10s cadence over two minutes: cpu = i, mem = 1000*(i+1),
	// root disk = 50+i.
	for i := 0; i < 12; i++ {
		mustInsert(t, s, snap(base.Add(time.Duration(i)*10*time.Second),
			float64(i), uint64(1000*(i+1)), 50+float64(i)))
	}

	r := Range{Key: "test", Dur: time.Hour, Bucket: time.Minute}
	pts := mustQuery(t, s, r, base.Add(2*time.Minute))
	if len(pts) != 2 {
		t.Fatalf("got %d buckets, want 2", len(pts))
	}

	// Bucket starts: base is minute-aligned, so buckets begin exactly there.
	if pts[0].TS != base.UnixMilli() || pts[1].TS != base.Add(time.Minute).UnixMilli() {
		t.Errorf("bucket starts = %d, %d; want %d, %d",
			pts[0].TS, pts[1].TS, base.UnixMilli(), base.Add(time.Minute).UnixMilli())
	}

	// Bucket 0 holds i=0..5, bucket 1 holds i=6..11.
	if !approx(pts[0].CPUPct, 2.5) || !approx(pts[0].CPUMaxPct, 5) {
		t.Errorf("bucket 0 cpu avg/max = %v/%v, want 2.5/5", pts[0].CPUPct, pts[0].CPUMaxPct)
	}
	if !approx(pts[1].CPUPct, 8.5) || !approx(pts[1].CPUMaxPct, 11) {
		t.Errorf("bucket 1 cpu avg/max = %v/%v, want 8.5/11", pts[1].CPUPct, pts[1].CPUMaxPct)
	}
	if pts[0].MemUsed != 3500 { // avg of 1000..6000
		t.Errorf("bucket 0 memUsed = %d, want 3500", pts[0].MemUsed)
	}
	if pts[0].SwapUsed != 100 {
		t.Errorf("bucket 0 swapUsed = %d, want 100", pts[0].SwapUsed)
	}

	// Disk merge: per-mount averages in every bucket, map non-nil.
	if !approx(pts[0].Disks["/"], 52.5) || !approx(pts[0].Disks["/data"], 10) {
		t.Errorf("bucket 0 disks = %v, want /=52.5 /data=10", pts[0].Disks)
	}
	if !approx(pts[1].Disks["/"], 58.5) {
		t.Errorf("bucket 1 disks = %v, want /=58.5", pts[1].Disks)
	}

	// GPU-less host: all pointers nil.
	if pts[0].GPUUtilPct != nil || pts[0].GPUMaxPct != nil ||
		pts[0].GPUMemUsed != nil || pts[0].GPUTempC != nil {
		t.Errorf("GPU fields should be nil on GPU-less data: %+v", pts[0])
	}
}

func TestQueryBucketStartUnaligned(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 1, 2, 3, 0, 7, 0, time.UTC) // not minute-aligned
	mustInsert(t, s, snap(base, 10, 1000, 50))

	r := Range{Key: "test", Dur: time.Hour, Bucket: time.Minute}
	pts := mustQuery(t, s, r, base.Add(time.Second))
	if len(pts) != 1 {
		t.Fatalf("got %d points, want 1", len(pts))
	}
	ts := base.UnixMilli()
	bucketMs := time.Minute.Milliseconds()
	if want := (ts / bucketMs) * bucketMs; pts[0].TS != want {
		t.Errorf("bucket TS = %d, want bucket start %d", pts[0].TS, want)
	}
}

func TestQueryWindow(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	mustInsert(t, s,
		snap(now.Add(-2*time.Hour), 99, 1000, 50), // outside 1h window
		snap(now.Add(-30*time.Minute), 10, 1000, 50),
	)
	pts := mustQuery(t, s, Ranges["1h"], now)
	if len(pts) != 1 {
		t.Fatalf("got %d points, want 1", len(pts))
	}
	if !approx(pts[0].CPUPct, 10) {
		t.Errorf("cpu = %v, want the in-window sample (10)", pts[0].CPUPct)
	}
}

// TestQueryExcludesFutureRows guards the upper time bound: after a corrected
// forward clock jump, rows timestamped past now must not leak into any range.
func TestQueryExcludesFutureRows(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	mustInsert(t, s,
		snap(now.Add(-30*time.Minute), 10, 1000, 50),
		snap(now.Add(30*time.Minute), 99, 9000, 90), // future-timestamped
	)
	pts := mustQuery(t, s, Ranges["1h"], now)
	if len(pts) != 1 {
		t.Fatalf("got %d points, want 1 (future row must be excluded)", len(pts))
	}
	if !approx(pts[0].CPUPct, 10) {
		t.Errorf("cpu = %v, want the past sample (10)", pts[0].CPUPct)
	}
}

func TestQueryContextCancelled(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	mustInsert(t, s, snap(base, 10, 1000, 50))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.QueryContext(ctx, Ranges["1h"], base.Add(time.Minute)); !errors.Is(err, context.Canceled) {
		t.Fatalf("QueryContext with cancelled ctx: err = %v, want context.Canceled", err)
	}
}

func TestQueryEmpty(t *testing.T) {
	s := newTestStore(t)
	pts := mustQuery(t, s, Ranges["1h"], time.Now())
	if pts == nil {
		t.Fatal("Query returned nil slice, want non-nil empty")
	}
	if len(pts) != 0 {
		t.Fatalf("got %d points, want 0", len(pts))
	}
}

func TestGPUAggregation(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)

	sn := metrics.Snapshot{
		TS:  base.UnixMilli(),
		CPU: metrics.CPUMetrics{UsagePct: 5},
		Mem: metrics.MemMetrics{Used: 1000, UsedPct: 10},
		GPUs: []metrics.GPUMetrics{
			{Index: 0, Name: "RTX A", UtilPct: 40, MemUsed: 1 << 30, TempC: 55},
			{Index: 1, Name: "RTX B", UtilPct: 60, MemUsed: 2 << 30, TempC: 70},
		},
	}
	mustInsert(t, s, sn)

	pts := mustQuery(t, s, Ranges["1h"], base.Add(time.Minute))
	if len(pts) != 1 {
		t.Fatalf("got %d points, want 1", len(pts))
	}
	p := pts[0]
	if p.GPUUtilPct == nil || !approx(*p.GPUUtilPct, 50) {
		t.Errorf("gpuUtilPct = %v, want 50 (avg across GPUs)", p.GPUUtilPct)
	}
	if p.GPUMaxPct == nil || !approx(*p.GPUMaxPct, 50) {
		// One row in the bucket: max == that row's avg-across-GPUs.
		t.Errorf("gpuMaxPct = %v, want 50", p.GPUMaxPct)
	}
	if p.GPUMemUsed == nil || *p.GPUMemUsed != 3<<30 {
		t.Errorf("gpuMemUsed = %v, want %d (sum across GPUs)", p.GPUMemUsed, uint64(3<<30))
	}
	if p.GPUTempC == nil || !approx(*p.GPUTempC, 70) {
		t.Errorf("gpuTempC = %v, want 70 (max across GPUs)", p.GPUTempC)
	}
	if p.Disks == nil {
		t.Error("Disks map is nil for a snapshot without disks, want non-nil empty")
	}
}

func TestGPUMixedBucket(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)

	withGPU := metrics.Snapshot{
		TS:   base.UnixMilli(),
		CPU:  metrics.CPUMetrics{UsagePct: 5},
		GPUs: []metrics.GPUMetrics{{UtilPct: 80, MemUsed: 1024, TempC: 60}},
	}
	withoutGPU := metrics.Snapshot{
		TS:  base.Add(10 * time.Second).UnixMilli(),
		CPU: metrics.CPUMetrics{UsagePct: 15},
	}
	mustInsert(t, s, withGPU, withoutGPU)

	r := Range{Key: "test", Dur: time.Hour, Bucket: time.Minute}
	pts := mustQuery(t, s, r, base.Add(time.Minute))
	if len(pts) != 1 {
		t.Fatalf("got %d points, want 1", len(pts))
	}
	p := pts[0]
	// SQL aggregates skip NULLs: the GPU-less row must not drag the average.
	if p.GPUUtilPct == nil || !approx(*p.GPUUtilPct, 80) {
		t.Errorf("gpuUtilPct = %v, want 80 (NULL rows excluded)", p.GPUUtilPct)
	}
	if !approx(p.CPUPct, 10) {
		t.Errorf("cpuPct = %v, want 10 (both rows averaged)", p.CPUPct)
	}
}

func TestInsertDuplicateTS(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)

	mustInsert(t, s, snap(base, 10, 1000, 50))
	// Same timestamp again (stalled collector): must not error, last wins.
	mustInsert(t, s, snap(base, 30, 2000, 60))

	pts := mustQuery(t, s, Ranges["1h"], base.Add(time.Minute))
	if len(pts) != 1 {
		t.Fatalf("got %d points, want 1", len(pts))
	}
	if !approx(pts[0].CPUPct, 30) || !approx(pts[0].Disks["/"], 60) {
		t.Errorf("cpu=%v disks=%v, want the replacing sample (30, /=60)",
			pts[0].CPUPct, pts[0].Disks)
	}
}

func TestPrune(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	mustInsert(t, s,
		snap(now.Add(-3*time.Hour), 99, 1000, 50),
		snap(now.Add(-time.Minute), 10, 1000, 50),
	)

	if err := s.Prune(time.Hour); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	pts := mustQuery(t, s, Ranges["24h"], now)
	if len(pts) != 1 {
		t.Fatalf("after prune got %d points, want 1", len(pts))
	}
	if !approx(pts[0].CPUPct, 10) {
		t.Errorf("surviving sample cpu = %v, want 10", pts[0].CPUPct)
	}

	// Both tables must be pruned, not just samples.
	for _, table := range []string{"samples", "disk_samples"} {
		var n int
		cutoff := now.Add(-time.Hour).UnixMilli()
		if err := s.db.QueryRow(
			`SELECT count(*) FROM `+table+` WHERE ts < ?`, cutoff).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s still has %d rows older than the cutoff", table, n)
		}
	}
}
