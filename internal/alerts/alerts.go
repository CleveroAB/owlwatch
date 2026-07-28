// Package alerts watches live snapshots and sends an email when a metric has
// been at or above its configured threshold for a configured duration
// (DESIGN.md §2, OWLWATCH_ALERT_* / OWLWATCH_SMTP_*).
//
// The evaluator is deliberately sample-driven and stateless across restarts:
// it keys off snapshot timestamps (not wall-clock ticks) so a lossy subscriber
// channel — the collector drops snapshots rather than block, see
// internal/collector — cannot stretch or shrink a breach window.
package alerts

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/CleveroAB/owlwatch/internal/metrics"
)

// Config carries the SMTP connection settings and the alert rules. The zero
// threshold disables a rule; Enabled reports whether alerting runs at all.
type Config struct {
	SMTPHost string
	SMTPPort int
	SMTPUser string // optional; PLAIN auth when set (requires STARTTLS unless localhost)
	SMTPPass string
	From     string
	To       []string

	CPUPct   float64 // total CPU usage %, 0 disables
	MemPct   float64 // memory used %, 0 disables
	DiskPct  float64 // per-mount disk used %, 0 disables
	GPUTempC float64 // per-GPU temperature °C, 0 disables

	For      time.Duration // how long a metric must stay at/above its threshold
	Cooldown time.Duration // minimum gap between two emails for the same rule
}

// Enabled reports whether alerting is configured. loadConfig guarantees that
// host, from and recipients are either all set or all empty.
func (c Config) Enabled() bool { return c.SMTPHost != "" && len(c.To) > 0 }

// retryAfter is how soon a rule may retry after a failed send — much sooner
// than the cooldown, so a transient SMTP error does not delay a real alert by
// half an hour, but not every 2s sample either.
const retryAfter = time.Minute

// ruleState is the per-rule breach tracker, keyed by rule key ("cpu",
// "disk:/var", "gpu0", ...).
type ruleState struct {
	since       time.Time // first sample at/above threshold; zero = currently below
	nextAllowed time.Time // no email for this rule before this instant
}

// breach is one rule that is due for notification on this evaluation pass.
type breach struct {
	state     *ruleState
	label     string
	value     float64
	threshold float64
	unit      string
	since     time.Time
}

// Notifier evaluates snapshots against Config and emails via a Mailer. It is
// not safe for concurrent use; Run is its single consumer.
type Notifier struct {
	cfg      Config
	hostname string
	mailer   Mailer
	states   map[string]*ruleState
	lastFail time.Time // rate-limits send-error logging
}

// New returns a Notifier that delivers over SMTP as configured.
func New(cfg Config, hostname string) *Notifier {
	return newNotifier(cfg, hostname, &smtpMailer{cfg: cfg})
}

func newNotifier(cfg Config, hostname string, m Mailer) *Notifier {
	return &Notifier{
		cfg:      cfg,
		hostname: hostname,
		mailer:   m,
		states:   make(map[string]*ruleState),
	}
}

// Run consumes snapshots until the context is cancelled or the channel
// closes. Sends happen inline: blocking this goroutine only drops snapshots
// (the collector fan-out is non-blocking), never the sampler.
func (n *Notifier) Run(ctx context.Context, snaps <-chan metrics.Snapshot) {
	for {
		select {
		case <-ctx.Done():
			return
		case snap, open := <-snaps:
			if !open {
				return
			}
			n.Evaluate(snap)
		}
	}
}

// Evaluate updates every rule's breach state from one snapshot and sends a
// single email covering all rules that became due.
func (n *Notifier) Evaluate(snap metrics.Snapshot) {
	now := time.UnixMilli(snap.TS)
	var due []breach
	n.check("cpu", "CPU usage", snap.CPU.UsagePct, n.cfg.CPUPct, "%", now, &due)
	n.check("mem", "Memory usage", snap.Mem.UsedPct, n.cfg.MemPct, "%", now, &due)
	for _, d := range snap.Disks {
		n.check("disk:"+d.Mount, "Disk "+d.Mount+" usage", d.UsedPct, n.cfg.DiskPct, "%", now, &due)
	}
	for _, g := range snap.GPUs {
		key := fmt.Sprintf("gpu%d", g.Index)
		label := fmt.Sprintf("GPU %d (%s) temperature", g.Index, g.Name)
		n.check(key, label, g.TempC, n.cfg.GPUTempC, "C", now, &due)
	}
	if len(due) == 0 {
		return
	}

	subject, body := n.compose(due)
	if err := n.mailer.Send(subject, body); err != nil {
		for _, d := range due {
			d.state.nextAllowed = now.Add(retryAfter)
		}
		if time.Since(n.lastFail) > time.Minute {
			n.lastFail = time.Now()
			log.Printf("alerts: send email: %v", err)
		}
		return
	}
	for _, d := range due {
		d.state.nextAllowed = now.Add(n.cfg.Cooldown)
	}
	log.Printf("alerts: emailed %d recipient(s): %s", len(n.cfg.To), subject)
}

// check advances one rule's state machine. A rule fires when it has been at
// or above its threshold for cfg.For and its cooldown has passed; any sample
// below the threshold resets the breach window.
func (n *Notifier) check(key, label string, value, threshold float64, unit string, now time.Time, due *[]breach) {
	if threshold <= 0 {
		return
	}
	st := n.states[key]
	if st == nil {
		st = &ruleState{}
		n.states[key] = st
	}
	if value < threshold {
		st.since = time.Time{}
		return
	}
	if st.since.IsZero() {
		st.since = now
	}
	if now.Sub(st.since) < n.cfg.For || now.Before(st.nextAllowed) {
		return
	}
	*due = append(*due, breach{
		state: st, label: label, value: value,
		threshold: threshold, unit: unit, since: st.since,
	})
}

// compose renders the fixed notification email. The layout is deliberately
// hard-coded: plain text, one line per breached metric.
func (n *Notifier) compose(due []breach) (subject, body string) {
	first := due[0]
	subject = fmt.Sprintf("[owlwatch] %s: %s at %.1f%s (threshold %.0f%s)",
		n.hostname, first.label, first.value, first.unit, first.threshold, first.unit)
	if len(due) > 1 {
		subject = fmt.Sprintf("[owlwatch] %s: %d metrics over threshold", n.hostname, len(due))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "owlwatch alert for %s\r\n\r\n", n.hostname)
	fmt.Fprintf(&b, "The following metrics have been at or above their threshold for at least %s:\r\n\r\n", n.cfg.For)
	for _, d := range due {
		fmt.Fprintf(&b, "  - %s: %.1f%s (threshold %.0f%s, breached since %s)\r\n",
			d.label, d.value, d.unit, d.threshold, d.unit, d.since.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "\r\nNo further email is sent for a metric while it stays breached until %s has passed.\r\n", n.cfg.Cooldown)
	return subject, b.String()
}
