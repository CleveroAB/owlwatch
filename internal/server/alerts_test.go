package server

import (
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

func newAlertsServer(token string, sender alertSender) *Server {
	col := collector.New(collector.Config{SampleInterval: time.Second})
	s := New(Config{Collector: col, Host: col.HostInfo(), SampleInterval: time.Second, Token: token})
	s.alerts = sender // seam: never a real SMTP connection in tests
	return s
}

func postTest(t *testing.T, s *Server, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/alerts/test", nil)
	req.Host = "127.0.0.1:8080"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func TestAlertsTestSendsExactlyOnce(t *testing.T) {
	sender := &fakeAlertSender{}
	rec := postTest(t, newAlertsServer(testToken, sender), testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if sender.calls != 1 {
		t.Fatalf("SendTest called %d times, want 1", sender.calls)
	}
}

func TestAlertsTestUnconfiguredIs409(t *testing.T) {
	rec := postTest(t, newAlertsServer(testToken, nil), testToken)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not configured") {
		t.Fatalf("body = %s, want a not-configured error", rec.Body)
	}
}

func TestAlertsTestSendFailureIs502(t *testing.T) {
	sender := &fakeAlertSender{err: errors.New("smtp: auth failed")}
	rec := postTest(t, newAlertsServer(testToken, sender), testToken)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "auth failed") {
		t.Fatalf("body = %s, want the send error surfaced", rec.Body)
	}
}

// A wrong token is rejected by the /api/ gate, and without OWLWATCH_TOKEN
// the route refuses outright — an unauthenticated POST cannot trigger emails.
func TestAlertsTestRequiresToken(t *testing.T) {
	sender := &fakeAlertSender{}
	if rec := postTest(t, newAlertsServer(testToken, sender), "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want 401", rec.Code)
	}
	if rec := postTest(t, newAlertsServer("", sender), ""); rec.Code != http.StatusForbidden {
		t.Fatalf("no token status = %d, want 403", rec.Code)
	}
	if sender.calls != 0 {
		t.Fatalf("SendTest called %d times, want 0", sender.calls)
	}
}
