package collector

import (
	"math"
	"testing"
	"time"

	"github.com/CleveroAB/owlwatch/internal/metrics"
)

func reading(pid int32, created int64, cpuSeconds float64, rss uint64) processReading {
	return processReading{pid: pid, name: "p", rss: rss, cpu: processCPUTime{created: created, seconds: cpuSeconds}}
}

func TestProcessSamplerRanksCPUBetweenWalks(t *testing.T) {
	var ps processSampler
	start := time.Unix(1000, 0)

	ps.update([]processReading{
		reading(1, 100, 10, 1<<20),
		reading(2, 200, 50, 2<<20),
		reading(3, 300, 5, 0),
		reading(4, 400, 1, 1<<20),
	}, start, 4, 100<<20)
	if cpu, mem := ps.rankings(); len(cpu) != 0 || len(mem) != 3 {
		t.Fatalf("first walk: cpu = %v, mem = %v; want no CPU rows (baseline only) and 3 memory rows", cpu, mem)
	}

	ps.update([]processReading{
		reading(1, 100, 30, 1<<20), // 20 s over 10 s on 4 cores = 50%
		reading(2, 200, 54, 2<<20), // 4 s = 10%
		reading(3, 300, 5, 0),      // idle: omitted
		reading(4, 999, 9, 1<<20),  // PID reused by a new process: no baseline
		reading(5, 500, 3, 1<<20),  // new since the last walk: no baseline
	}, start.Add(10*time.Second), 4, 100<<20)

	cpu, _ := ps.rankings()
	want := []metrics.ProcessCPUMetrics{
		{PID: 1, Name: "p", UsagePct: 50},
		{PID: 2, Name: "p", UsagePct: 10},
	}
	if len(cpu) != len(want) {
		t.Fatalf("cpu ranking = %v, want %v", cpu, want)
	}
	for i := range want {
		if cpu[i].PID != want[i].PID || math.Abs(cpu[i].UsagePct-want[i].UsagePct) > 1e-9 {
			t.Fatalf("cpu ranking = %v, want %v", cpu, want)
		}
	}
}

func TestProcessSamplerClampsCPUShare(t *testing.T) {
	var ps processSampler
	start := time.Unix(1000, 0)
	ps.update([]processReading{reading(1, 100, 0, 1)}, start, 1, 1)
	ps.update([]processReading{reading(1, 100, 50, 1)}, start.Add(10*time.Second), 1, 1)
	if cpu, _ := ps.rankings(); len(cpu) != 1 || cpu[0].UsagePct != 100 {
		t.Fatalf("cpu ranking = %v, want one row clamped to 100%%", cpu)
	}
}

func TestRankCPUProcessesOrdersAndLimits(t *testing.T) {
	rows := make([]metrics.ProcessCPUMetrics, 12)
	for i := range rows {
		rows[i] = metrics.ProcessCPUMetrics{PID: int32(20 - i), UsagePct: float64(i / 2)}
	}
	got := rankCPUProcesses(rows, 10)
	if len(got) != 10 {
		t.Fatalf("rankCPUProcesses() returned %d rows, want 10", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].UsagePct < got[i].UsagePct ||
			(got[i-1].UsagePct == got[i].UsagePct && got[i-1].PID > got[i].PID) {
			t.Fatalf("rankCPUProcesses() order wrong at %d: %v", i, got)
		}
	}
}
