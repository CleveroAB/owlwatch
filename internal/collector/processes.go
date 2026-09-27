package collector

import (
	"cmp"
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/CleveroAB/owlwatch/internal/metrics"
)

// processSampler holds the per-process state the rankings need between walks.
// CPU usage is a rate, so each walk compares every process's accumulated CPU
// time with the previous walk's.
type processSampler struct {
	next     time.Time // earliest time of the next walk
	lastWalk time.Time // zero until the first walk has recorded a baseline
	cpuTimes map[int32]processCPUTime
	topCPU   []metrics.ProcessCPUMetrics
	topMem   []metrics.ProcessMemoryMetrics
}

// processCPUTime is one process's accumulated user+system CPU seconds. The
// creation time tells a new process apart from an old one whose PID it reuses.
type processCPUTime struct {
	created int64 // unix ms
	seconds float64
}

// processReading is what one walk learns about one process.
type processReading struct {
	pid  int32
	name string
	rss  uint64
	cpu  processCPUTime
}

// sampleProcesses returns the cached CPU and resident-memory rankings,
// refreshing them with a fresh walk of the process table at most every
// processSampleInterval. Processes can exit while /proc is being walked, so
// failures for individual rows are expected and skipped.
func (c *Collector) sampleProcesses(ctx context.Context, cores int, totalMemory uint64) ([]metrics.ProcessCPUMetrics, []metrics.ProcessMemoryMetrics) {
	ps := &c.procs
	now := time.Now()
	if now.Before(ps.next) {
		return ps.rankings()
	}
	ps.next = now.Add(processSampleInterval)

	probeCtx, cancel := context.WithTimeout(ctx, processSampleTimeout)
	defer cancel()
	processes, err := process.ProcessesWithContext(probeCtx)
	if err != nil {
		c.errlog.printf("processes", "collector: listing processes: %v", err)
		return ps.rankings()
	}

	readings := make([]processReading, 0, len(processes))
	for _, p := range processes {
		if probeCtx.Err() != nil {
			break
		}
		if r, ok := readProcess(probeCtx, p); ok {
			readings = append(readings, r)
		}
	}
	if cores <= 0 {
		cores = runtime.NumCPU()
	}
	if ps.lastWalk.IsZero() {
		// The first walk is only a CPU baseline; take the next one on the
		// following tick so the CPU ranking appears within seconds.
		ps.next = now
	}
	ps.update(readings, now, cores, totalMemory)
	return ps.rankings()
}

func readProcess(ctx context.Context, p *process.Process) (processReading, bool) {
	r := processReading{pid: p.Pid}
	if memory, err := p.MemoryInfoWithContext(ctx); err == nil && memory != nil {
		r.rss = memory.RSS
	}
	if times, err := p.TimesWithContext(ctx); err == nil && times != nil {
		r.cpu.seconds = times.User + times.System
	}
	if created, err := p.CreateTimeWithContext(ctx); err == nil {
		r.cpu.created = created
	}
	if r.rss == 0 && r.cpu.seconds == 0 {
		return r, false // kernel thread, or the process already exited
	}
	name, err := p.NameWithContext(ctx)
	if err != nil || strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("PID %d", p.Pid)
	}
	r.name = name
	return r, true
}

// update turns one walk's readings into both rankings and keeps the CPU
// times as the next walk's baseline. CPU shares are of the whole host (all
// cores), matching CPUMetrics.UsagePct, averaged since the previous walk.
func (ps *processSampler) update(readings []processReading, now time.Time, cores int, totalMemory uint64) {
	elapsed := now.Sub(ps.lastWalk).Seconds()
	haveBaseline := !ps.lastWalk.IsZero() && elapsed > 0 && cores > 0

	cpuRows := make([]metrics.ProcessCPUMetrics, 0, len(readings))
	memRows := make([]metrics.ProcessMemoryMetrics, 0, len(readings))
	cpuTimes := make(map[int32]processCPUTime, len(readings))
	for _, r := range readings {
		cpuTimes[r.pid] = r.cpu
		if r.rss > 0 {
			usedPct := 0.0
			if totalMemory > 0 {
				usedPct = float64(r.rss) / float64(totalMemory) * 100
			}
			memRows = append(memRows, metrics.ProcessMemoryMetrics{
				PID: r.pid, Name: r.name, Used: r.rss, UsedPct: usedPct,
			})
		}
		prev, ok := ps.cpuTimes[r.pid]
		if !haveBaseline || !ok || prev.created != r.cpu.created {
			continue // no earlier reading of this same process to compare with
		}
		if delta := r.cpu.seconds - prev.seconds; delta > 0 {
			cpuRows = append(cpuRows, metrics.ProcessCPUMetrics{
				PID: r.pid, Name: r.name, UsagePct: min(delta/elapsed/float64(cores)*100, 100),
			})
		}
	}
	ps.cpuTimes = cpuTimes
	ps.lastWalk = now
	ps.topCPU = rankCPUProcesses(cpuRows, topProcessCount)
	ps.topMem = rankProcesses(memRows, topProcessCount)
}

// rankings returns copies of the cached rankings, never nil.
func (ps *processSampler) rankings() ([]metrics.ProcessCPUMetrics, []metrics.ProcessMemoryMetrics) {
	return append([]metrics.ProcessCPUMetrics{}, ps.topCPU...),
		append([]metrics.ProcessMemoryMetrics{}, ps.topMem...)
}

// rankTop sorts rows largest first by key, breaking ties by lower PID, and
// keeps at most limit rows.
func rankTop[T any, K cmp.Ordered](rows []T, limit int, key func(T) K, pid func(T) int32) []T {
	slices.SortFunc(rows, func(a, b T) int {
		return cmp.Or(cmp.Compare(key(b), key(a)), cmp.Compare(pid(a), pid(b)))
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func rankProcesses(rows []metrics.ProcessMemoryMetrics, limit int) []metrics.ProcessMemoryMetrics {
	return rankTop(rows, limit,
		func(r metrics.ProcessMemoryMetrics) uint64 { return r.Used },
		func(r metrics.ProcessMemoryMetrics) int32 { return r.PID })
}

func rankCPUProcesses(rows []metrics.ProcessCPUMetrics, limit int) []metrics.ProcessCPUMetrics {
	return rankTop(rows, limit,
		func(r metrics.ProcessCPUMetrics) float64 { return r.UsagePct },
		func(r metrics.ProcessCPUMetrics) int32 { return r.PID })
}
