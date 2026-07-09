package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHostAllowed(t *testing.T) {
	allowed := []string{"owlwatch.lan", "Monitor.Example"}
	tests := []struct {
		host string
		want bool
	}{
		{"", true},             // HTTP/1.0 request without a Host header
		{"192.168.1.20", true}, // IPv4 literal
		{"192.168.1.20:8080", true},
		{"[::1]", true}, // bare bracketed IPv6 literal
		{"[fe80::1]:8080", true},
		{"::1", true}, // unbracketed IPv6 from a lenient client
		{"localhost", true},
		{"localhost:8080", true},
		{"LOCALHOST", true},
		{"owlwatch.localhost", true}, // *.localhost resolves to loopback
		{"owlwatch.lan", true},       // allowlisted
		{"OWLWATCH.LAN:8080", true},  // allowlist match is case-insensitive
		{"monitor.example", true},
		{"attacker.example", false}, // rebinding attempt
		{"attacker.example:8080", false},
		{"localhost.attacker.example", false},
		{"notlocalhost", false}, // suffix must be ".localhost", not "localhost"
	}
	for _, tt := range tests {
		if got := hostAllowed(tt.host, allowed); got != tt.want {
			t.Errorf("hostAllowed(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}

func TestWithHostCheck(t *testing.T) {
	h := withHostCheck(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/host", nil)
	req.Host = "attacker.example"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Errorf("rebound host: status = %d, want %d", rec.Code, http.StatusMisdirectedRequest)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("rebound host: Content-Type = %q, want text/plain", ct)
	}

	req.Host = "127.0.0.1:8080" // what `owlwatch -healthcheck` sends
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("IP-literal host: status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

// deadlineWriter fakes a response writer with write-deadline support,
// recording the deadline it was armed with.
type deadlineWriter struct {
	http.ResponseWriter
	deadline time.Time
}

func (d *deadlineWriter) SetWriteDeadline(t time.Time) error {
	d.deadline = t
	return nil
}

// The SSE handler's per-write deadlines only reap stalled peers if
// http.ResponseController can see through statusRecorder to the real
// connection; that is what Unwrap is for.
func TestStatusRecorderUnwrapReachesSetWriteDeadline(t *testing.T) {
	inner := &deadlineWriter{ResponseWriter: httptest.NewRecorder()}
	sr := &statusRecorder{ResponseWriter: inner}

	want := time.Now().Add(30 * time.Second)
	if err := http.NewResponseController(sr).SetWriteDeadline(want); err != nil {
		t.Fatalf("SetWriteDeadline through statusRecorder: %v", err)
	}
	if !inner.deadline.Equal(want) {
		t.Fatalf("inner deadline = %v, want %v", inner.deadline, want)
	}
}
