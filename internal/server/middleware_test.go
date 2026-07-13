package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CleveroAB/owlwatch/internal/collector"
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

func TestWithSecurityHeaders(t *testing.T) {
	h := withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	want := map[string]string{
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Permissions-Policy":           "camera=(), geolocation=(), microphone=()",
		"Referrer-Policy":              "no-referrer",
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
	}
	for name, value := range want {
		if got := rec.Header().Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "connect-src 'self'") {
		t.Errorf("Content-Security-Policy is incomplete: %q", csp)
	} else if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("Content-Security-Policy permits inline scripts: %q", csp)
	}
}

// Token auth (DESIGN.md §9.2): every /api/ route requires a Bearer header;
// URL query credentials are rejected, while /healthz and the UI stay
// open, and an empty configured token disables the check entirely.
func TestWithTokenAuth(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	tests := []struct {
		name   string
		token  string // configured token; "" disables the check
		target string // path (plus optional query) requested
		header string // Authorization header value; "" = none
		want   int
	}{
		{"no token configured", "", "/api/host", "", http.StatusNoContent},
		{"api without token", "s3cret", "/api/host", "", http.StatusUnauthorized},
		{"bare /api gated too", "s3cret", "/api", "", http.StatusUnauthorized},
		{"unknown api path gated", "s3cret", "/api/nope", "", http.StatusUnauthorized},
		{"valid bearer", "s3cret", "/api/host", "Bearer s3cret", http.StatusNoContent},
		{"wrong bearer", "s3cret", "/api/host", "Bearer nope", http.StatusUnauthorized},
		{"missing Bearer prefix", "s3cret", "/api/host", "s3cret", http.StatusUnauthorized},
		{"token as bearer prefix only", "s3cret", "/api/host", "Bearer s3cr", http.StatusUnauthorized},
		{"query tokens are rejected", "s3cret", "/api/servers/db1/live?token=s3cret", "", http.StatusUnauthorized},
		{"wrong query token", "s3cret", "/api/live?token=nope", "", http.StatusUnauthorized},
		{"wrong bearer, valid query", "s3cret", "/api/live?token=s3cret", "Bearer nope", http.StatusUnauthorized},
		{"healthz stays open", "s3cret", "/healthz", "", http.StatusNoContent},
		{"ui shell stays open", "s3cret", "/", "", http.StatusNoContent},
		{"ui assets stay open", "s3cret", "/assets/index-abc123.js", "", http.StatusNoContent},
		{"prefix must be /api/", "s3cret", "/apifoo", "", http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			withTokenAuth(tt.token, next).ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.want == http.StatusUnauthorized {
				if body := strings.TrimSpace(rec.Body.String()); body != `{"error":"unauthorized"}` {
					t.Errorf("body = %q, want the unauthorized JSON error", body)
				}
			}
		})
	}
}

func TestWithRequestLimits(t *testing.T) {
	for _, path := range []string{"/api/live", "/api/history"} {
		t.Run(path, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			h := withRequestLimits(1, 1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-release
				w.WriteHeader(http.StatusNoContent)
			}))

			firstDone := make(chan struct{})
			go func() {
				defer close(firstDone)
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
			}()
			<-entered

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("second request status = %d, want 429", rec.Code)
			}
			if got := rec.Header().Get("Retry-After"); got != "5" {
				t.Fatalf("Retry-After = %q, want 5", got)
			}
			close(release)
			<-firstDone
		})
	}
}

// The token check sits inside the host check: a rebound Host must get 421,
// not 401, so a hostile page cannot even probe whether auth is enabled.
func TestHostCheckWinsOverTokenAuth(t *testing.T) {
	col := collector.New(collector.Config{SampleInterval: time.Second})
	s := New(Config{Collector: col, SampleInterval: time.Second, Token: "s3cret"})

	req := httptest.NewRequest(http.MethodGet, "/api/host", nil)
	req.Host = "attacker.example"
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMisdirectedRequest)
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
