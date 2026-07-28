package alerts

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CleveroAB/owlwatch/internal/metrics"
)

type fakeMailer struct {
	subjects []string
	bodies   []string
	err      error // returned by Send when non-nil
}

func (f *fakeMailer) Send(subject, body string) error {
	if f.err != nil {
		return f.err
	}
	f.subjects = append(f.subjects, subject)
	f.bodies = append(f.bodies, body)
	return nil
}

func testConfig() Config {
	return Config{
		SMTPHost: "smtp.example.com",
		To:       []string{"ops@example.com"},
		CPUPct:   90,
		MemPct:   90,
		DiskPct:  92,
		GPUTempC: 90,
		For:      5 * time.Minute,
		Cooldown: 30 * time.Minute,
	}
}

// snapAt builds a snapshot with the given CPU usage at t.
func snapAt(t time.Time, cpuPct float64) metrics.Snapshot {
	return metrics.Snapshot{TS: t.UnixMilli(), CPU: metrics.CPUMetrics{UsagePct: cpuPct}}
}

var t0 = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

func TestNoEmailBelowThreshold(t *testing.T) {
	m := &fakeMailer{}
	n := newNotifier(testConfig(), "web1", m)
	for i := range 100 {
		n.Evaluate(snapAt(t0.Add(time.Duration(i)*10*time.Second), 89.9))
	}
	if len(m.subjects) != 0 {
		t.Fatalf("sent %d emails below threshold, want 0", len(m.subjects))
	}
}

func TestEmailOnlyAfterSustainedBreach(t *testing.T) {
	m := &fakeMailer{}
	n := newNotifier(testConfig(), "web1", m)

	n.Evaluate(snapAt(t0, 95))
	n.Evaluate(snapAt(t0.Add(4*time.Minute), 95))
	if len(m.subjects) != 0 {
		t.Fatalf("emailed before the For window elapsed")
	}

	n.Evaluate(snapAt(t0.Add(5*time.Minute), 95.5))
	if len(m.subjects) != 1 {
		t.Fatalf("sent %d emails after sustained breach, want 1", len(m.subjects))
	}
	if want := "[owlwatch] web1: CPU usage at 95.5% (threshold 90%)"; m.subjects[0] != want {
		t.Fatalf("subject = %q, want %q", m.subjects[0], want)
	}
	if !strings.Contains(m.bodies[0], "CPU usage: 95.5% (threshold 90%") {
		t.Fatalf("body missing metric line:\n%s", m.bodies[0])
	}
}

func TestDipResetsBreachWindow(t *testing.T) {
	m := &fakeMailer{}
	n := newNotifier(testConfig(), "web1", m)

	n.Evaluate(snapAt(t0, 95))
	n.Evaluate(snapAt(t0.Add(4*time.Minute), 80)) // dip below resets
	n.Evaluate(snapAt(t0.Add(5*time.Minute), 95))
	n.Evaluate(snapAt(t0.Add(9*time.Minute), 95)) // only 4m into the new breach
	if len(m.subjects) != 0 {
		t.Fatalf("emailed although the breach was interrupted")
	}
	n.Evaluate(snapAt(t0.Add(10*time.Minute), 95))
	if len(m.subjects) != 1 {
		t.Fatalf("sent %d emails, want 1 after the new sustained breach", len(m.subjects))
	}
}

func TestCooldownSuppressesRepeats(t *testing.T) {
	m := &fakeMailer{}
	n := newNotifier(testConfig(), "web1", m)

	for i := range 60 { // 60 samples, one per minute: breached the whole hour
		n.Evaluate(snapAt(t0.Add(time.Duration(i)*time.Minute), 95))
	}
	// First email at t0+5m (For elapsed), second at t0+35m (cooldown elapsed),
	// third would come at t0+65m which is past the last sample.
	if len(m.subjects) != 2 {
		t.Fatalf("sent %d emails over a 1h continuous breach, want 2", len(m.subjects))
	}
}

func TestOneEmailCoversMultipleRules(t *testing.T) {
	m := &fakeMailer{}
	n := newNotifier(testConfig(), "web1", m)

	snap := func(ts time.Time) metrics.Snapshot {
		return metrics.Snapshot{
			TS:  ts.UnixMilli(),
			CPU: metrics.CPUMetrics{UsagePct: 95},
			Mem: metrics.MemMetrics{UsedPct: 93},
			Disks: []metrics.DiskMetrics{
				{Mount: "/", UsedPct: 50},
				{Mount: "/var", UsedPct: 97},
			},
			GPUs: []metrics.GPUMetrics{{Index: 0, Name: "RTX 4090", TempC: 91}},
		}
	}
	n.Evaluate(snap(t0))
	n.Evaluate(snap(t0.Add(5 * time.Minute)))
	if len(m.subjects) != 1 {
		t.Fatalf("sent %d emails, want 1 covering all breached rules", len(m.subjects))
	}
	if want := "[owlwatch] web1: 4 metrics over threshold"; m.subjects[0] != want {
		t.Fatalf("subject = %q, want %q", m.subjects[0], want)
	}
	body := m.bodies[0]
	for _, line := range []string{"CPU usage", "Memory usage", "Disk /var usage", "GPU 0 (RTX 4090) temperature"} {
		if !strings.Contains(body, line) {
			t.Fatalf("body missing %q:\n%s", line, body)
		}
	}
	if strings.Contains(body, "Disk / usage") {
		t.Fatalf("body includes the healthy root disk:\n%s", body)
	}
}

func TestZeroThresholdDisablesRule(t *testing.T) {
	cfg := testConfig()
	cfg.CPUPct = 0
	m := &fakeMailer{}
	n := newNotifier(cfg, "web1", m)

	n.Evaluate(snapAt(t0, 100))
	n.Evaluate(snapAt(t0.Add(10*time.Minute), 100))
	if len(m.subjects) != 0 {
		t.Fatalf("CPU rule fired although its threshold is 0 (disabled)")
	}
}

func TestFailedSendRetriesBeforeCooldown(t *testing.T) {
	m := &fakeMailer{err: errors.New("connection refused")}
	n := newNotifier(testConfig(), "web1", m)

	n.Evaluate(snapAt(t0, 95))
	n.Evaluate(snapAt(t0.Add(5*time.Minute), 95)) // due, but send fails
	m.err = nil
	n.Evaluate(snapAt(t0.Add(5*time.Minute+30*time.Second), 95)) // within retryAfter
	if len(m.subjects) != 0 {
		t.Fatalf("retried before the retry backoff elapsed")
	}
	n.Evaluate(snapAt(t0.Add(7*time.Minute), 95)) // past retryAfter, well before cooldown
	if len(m.subjects) != 1 {
		t.Fatalf("sent %d emails, want 1 on the post-failure retry", len(m.subjects))
	}
}
