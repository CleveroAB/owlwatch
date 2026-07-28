package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CleveroAB/owlwatch/internal/collector"
)

type fakeAlertSender struct {
	err   error
	calls int
}

func (f *fakeAlertSender) SendTest() error {
	f.calls++
	return f.err
}

func newAlertsServer(sender alertSender) *Server {
	col := collector.New(collector.Config{SampleInterval: time.Second})
	s := New(Config{Collector: col, Host: col.HostInfo(), SampleInterval: time.Second})
	s.alerts = sender // seam: never a real SMTP connection in tests
	return s
}

func do(t *testing.T, s *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Host = "127.0.0.1:8080"
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func TestAlertsStatusReflectsConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name   string
		sender alertSender
		want   bool
	}{
		{"disabled", nil, false},
		{"enabled", &fakeAlertSender{}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, newAlertsServer(tt.sender), http.MethodGet, "/api/alerts")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			var body struct{ Enabled bool }
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if body.Enabled != tt.want {
				t.Fatalf("enabled = %v, want %v", body.Enabled, tt.want)
			}
		})
	}
}

func TestAlertsTestSendsExactlyOnce(t *testing.T) {
	sender := &fakeAlertSender{}
	rec := do(t, newAlertsServer(sender), http.MethodPost, "/api/alerts/test")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if sender.calls != 1 {
		t.Fatalf("SendTest called %d times, want 1", sender.calls)
	}
}

func TestAlertsTestUnconfiguredIs409(t *testing.T) {
	rec := do(t, newAlertsServer(nil), http.MethodPost, "/api/alerts/test")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not configured") {
		t.Fatalf("body = %s, want a not-configured error", rec.Body)
	}
}

func TestAlertsTestSendFailureIs502(t *testing.T) {
	sender := &fakeAlertSender{err: errors.New("smtp: auth failed")}
	rec := do(t, newAlertsServer(sender), http.MethodPost, "/api/alerts/test")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "auth failed") {
		t.Fatalf("body = %s, want the send error surfaced", rec.Body)
	}
}

// The test route is under /api/, so OWLWATCH_TOKEN must gate it like
// everything else — an unauthenticated POST cannot trigger emails.
func TestAlertsTestRequiresToken(t *testing.T) {
	col := collector.New(collector.Config{SampleInterval: time.Second})
	s := New(Config{Collector: col, SampleInterval: time.Second, Token: "0123456789abcdef"})
	s.alerts = &fakeAlertSender{}
	rec := do(t, s, http.MethodPost, "/api/alerts/test")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
