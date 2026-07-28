package main

import (
	"strings"
	"testing"
	"time"
)

var configEnvKeys = []string{
	"OWLWATCH_LISTEN", "OWLWATCH_PORT", "OWLWATCH_DB",
	"OWLWATCH_SAMPLE_INTERVAL", "OWLWATCH_PERSIST_INTERVAL",
	"OWLWATCH_RETENTION_DAYS", "OWLWATCH_ROOTFS", "OWLWATCH_ALLOWED_HOSTS",
	"OWLWATCH_PEERS", "OWLWATCH_TOKEN", "OWLWATCH_MAX_SSE_CLIENTS",
	"OWLWATCH_MAX_HISTORY_REQUESTS",
	"OWLWATCH_SMTP_HOST", "OWLWATCH_SMTP_PORT", "OWLWATCH_SMTP_USER",
	"OWLWATCH_SMTP_PASS", "OWLWATCH_SMTP_FROM", "OWLWATCH_ALERT_TO",
	"OWLWATCH_ALERT_CPU_PCT", "OWLWATCH_ALERT_MEM_PCT",
	"OWLWATCH_ALERT_DISK_PCT", "OWLWATCH_ALERT_GPU_TEMP_C",
	"OWLWATCH_ALERT_FOR", "OWLWATCH_ALERT_COOLDOWN",
}

func cleanConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range configEnvKeys {
		t.Setenv(key, "")
	}
}

func TestLoadConfigSecureDefaults(t *testing.T) {
	cleanConfigEnv(t)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.listenAddress != "127.0.0.1" || cfg.port != 8080 {
		t.Fatalf("listen default = %s:%d, want 127.0.0.1:8080", cfg.listenAddress, cfg.port)
	}
	if cfg.maxSSEClients != 128 || cfg.maxHistory != 16 {
		t.Fatalf("request limits = %d/%d, want 128/16", cfg.maxSSEClients, cfg.maxHistory)
	}
}

func TestLoadConfigAcceptsBoundedOverrides(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("OWLWATCH_LISTEN", "0.0.0.0")
	t.Setenv("OWLWATCH_PORT", "9090")
	t.Setenv("OWLWATCH_SAMPLE_INTERVAL", "250ms")
	t.Setenv("OWLWATCH_PERSIST_INTERVAL", "250ms")
	t.Setenv("OWLWATCH_RETENTION_DAYS", "3650")
	t.Setenv("OWLWATCH_TOKEN", "0123456789abcdef")
	t.Setenv("OWLWATCH_MAX_SSE_CLIENTS", "1")
	t.Setenv("OWLWATCH_MAX_HISTORY_REQUESTS", "1000")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.listenAddress != "0.0.0.0" || cfg.port != 9090 || cfg.sampleInterval != 250*time.Millisecond {
		t.Fatalf("overrides were not applied: %+v", cfg)
	}
}

func TestLoadConfigAlertDefaults(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("OWLWATCH_SMTP_HOST", "smtp.example.com")
	t.Setenv("OWLWATCH_SMTP_FROM", "owlwatch@example.com")
	t.Setenv("OWLWATCH_ALERT_TO", "ops@example.com, oncall@example.com")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	a := cfg.alerts
	if !a.Enabled() {
		t.Fatalf("alerts not enabled with host, from and recipients set")
	}
	if len(a.To) != 2 || a.To[1] != "oncall@example.com" {
		t.Fatalf("recipients = %v, want two trimmed addresses", a.To)
	}
	if a.SMTPPort != 587 {
		t.Fatalf("SMTP port default = %d, want 587", a.SMTPPort)
	}
	if a.CPUPct != 90 || a.MemPct != 90 || a.DiskPct != 92 || a.GPUTempC != 90 {
		t.Fatalf("threshold defaults = %+v, want 90/90/92/90", a)
	}
	if a.For != 5*time.Minute || a.Cooldown != 30*time.Minute {
		t.Fatalf("for/cooldown defaults = %s/%s, want 5m/30m", a.For, a.Cooldown)
	}
}

func TestLoadConfigAlertRequiresSender(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("OWLWATCH_SMTP_HOST", "smtp.example.com")
	t.Setenv("OWLWATCH_ALERT_TO", "ops@example.com")
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "OWLWATCH_SMTP_FROM: required") {
		t.Fatalf("loadConfig error = %v, want missing-sender error", err)
	}
}

func TestLoadConfigAlertsDisabledByDefault(t *testing.T) {
	cleanConfigEnv(t)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.alerts.Enabled() {
		t.Fatalf("alerts enabled without any OWLWATCH_SMTP_*/OWLWATCH_ALERT_* config")
	}
}

func TestLoadConfigRejectsUnsafeOrPathologicalValues(t *testing.T) {
	tests := []struct {
		name string
		key  string
		val  string
		want string
	}{
		{"non-IP listen address", "OWLWATCH_LISTEN", "public.example", "want an IP address"},
		{"sample too fast", "OWLWATCH_SAMPLE_INTERVAL", "1ns", "want 250ms..1m"},
		{"persist faster than sample", "OWLWATCH_PERSIST_INTERVAL", "1s", "want sample interval..1h"},
		{"retention too large", "OWLWATCH_RETENTION_DAYS", "3651", "want 1..3650"},
		{"short token", "OWLWATCH_TOKEN", "short", "at least 16 characters"},
		{"zero SSE limit", "OWLWATCH_MAX_SSE_CLIENTS", "0", "want 1..10000"},
		{"history limit too large", "OWLWATCH_MAX_HISTORY_REQUESTS", "1001", "want 1..1000"},
		{"recipients without SMTP host", "OWLWATCH_ALERT_TO", "ops@example.com", "OWLWATCH_SMTP_HOST: required"},
		{"SMTP host without recipients", "OWLWATCH_SMTP_HOST", "smtp.example.com", "OWLWATCH_ALERT_TO: required"},
		{"CPU threshold over 100", "OWLWATCH_ALERT_CPU_PCT", "150", "want 0..100"},
		{"non-numeric memory threshold", "OWLWATCH_ALERT_MEM_PCT", "lots", "want 0..100"},
		{"negative alert duration", "OWLWATCH_ALERT_FOR", "-5m", "want a positive Go duration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanConfigEnv(t)
			t.Setenv(tt.key, tt.val)
			_, err := loadConfig()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("loadConfig error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
