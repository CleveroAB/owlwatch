package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CleveroAB/owlwatch/internal/collector"
	"github.com/CleveroAB/owlwatch/internal/metrics"
	"github.com/CleveroAB/owlwatch/internal/peers"
)

const testToken = "0123456789abcdef"

func rebootRequest(t *testing.T, s *Server, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Host = "127.0.0.1:8080"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func newRebootServer(token string, reboot func(), fleet *fakeFleet) *Server {
	col := collector.New(collector.Config{SampleInterval: time.Second})
	s := New(Config{
		Collector:      col,
		Host:           col.HostInfo(),
		SampleInterval: time.Second,
		Token:          token,
		Reboot:         reboot,
	})
	if fleet != nil {
		s.peers = fleet
	}
	return s
}

// Without OWLWATCH_TOKEN anyone reaching the port could restart owlwatch in
// a loop, so the route refuses outright — locally and when proxying a peer.
func TestRebootRefusedWithoutToken(t *testing.T) {
	var calls atomic.Int32
	fleet := &fakeFleet{
		servers: []metrics.ServerSummary{{ID: "web1", Name: "Web One", Online: true}},
		reboot: func(context.Context, string) error {
			calls.Add(1)
			return nil
		},
	}
	s := newRebootServer("", func() { calls.Add(1) }, fleet)
	for _, path := range []string{"/api/reboot", "/api/servers/local/reboot", "/api/servers/web1/reboot"} {
		rec := rebootRequest(t, s, path, "")
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "OWLWATCH_TOKEN") {
			t.Fatalf("%s: response = %d %s, want 403 naming OWLWATCH_TOKEN", path, rec.Code, rec.Body)
		}
	}
	time.Sleep(150 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("reboot calls = %d, want 0", calls.Load())
	}
}

func TestRebootRejectsWrongToken(t *testing.T) {
	var calls atomic.Int32
	rec := rebootRequest(t, newRebootServer(testToken, func() { calls.Add(1) }, nil), "/api/reboot", "wrong")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	time.Sleep(150 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("reboot calls = %d, want 0", calls.Load())
	}
}

func TestRebootUnavailable(t *testing.T) {
	rec := rebootRequest(t, newRebootServer(testToken, nil, nil), "/api/reboot", testToken)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "unavailable") {
		t.Fatalf("response = %d %s, want 503 unavailable", rec.Code, rec.Body)
	}
}

func TestRebootAcceptedExactlyOnce(t *testing.T) {
	calls := make(chan struct{}, 2)
	s := newRebootServer(testToken, func() { calls <- struct{}{} }, nil)
	for range 2 {
		rec := rebootRequest(t, s, "/api/servers/local/reboot", testToken)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202", rec.Code)
		}
	}
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("reboot callback was not called")
	}
	select {
	case <-calls:
		t.Fatal("reboot callback was called more than once")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestServerRebootProxiesPeer(t *testing.T) {
	called := ""
	fleet := &fakeFleet{
		servers: []metrics.ServerSummary{{ID: "web1", Name: "Web One", Online: true}},
		reboot: func(_ context.Context, id string) error {
			called = id
			return nil
		},
	}
	rec := rebootRequest(t, newRebootServer(testToken, nil, fleet), "/api/servers/web1/reboot", testToken)
	if rec.Code != http.StatusAccepted || called != "web1" {
		t.Fatalf("response = %d, called = %q; want 202 and web1", rec.Code, called)
	}
}

func TestServerRebootMapsPeerFailures(t *testing.T) {
	var peerErr error
	fleet := &fakeFleet{
		servers: []metrics.ServerSummary{{ID: "web1", Name: "Web One", Online: true}},
		reboot:  func(context.Context, string) error { return peerErr },
	}
	s := newRebootServer(testToken, nil, fleet)

	peerErr = peers.ErrPeerUnavailable
	rec := rebootRequest(t, s, "/api/servers/web1/reboot", testToken)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("unavailable status = %d, want 502", rec.Code)
	}

	peerErr = &peers.RefusedError{Reason: errActionsNeedToken}
	rec = rebootRequest(t, s, "/api/servers/web1/reboot", testToken)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "refused the restart: this action requires OWLWATCH_TOKEN") {
		t.Fatalf("refused response = %d %s, want 502 carrying the peer's reason", rec.Code, rec.Body)
	}

	rec = rebootRequest(t, s, "/api/servers/ghost/reboot", testToken)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d, want 404", rec.Code)
	}
}

func TestActionsReflectTokenAndConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name   string
		token  string
		reboot func()
		sender alertSender
		want   actionsResponse
	}{
		{"no token", "", func() {}, &fakeAlertSender{}, actionsResponse{}},
		{"token, nothing configured", testToken, nil, nil, actionsResponse{}},
		{"token, all configured", testToken, func() {}, &fakeAlertSender{}, actionsResponse{Restart: true, TestEmail: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newRebootServer(tt.token, tt.reboot, nil)
			s.alerts = tt.sender
			req := httptest.NewRequest(http.MethodGet, "/api/actions", nil)
			req.Host = "127.0.0.1:8080"
			req.Header.Set("Authorization", "Bearer "+tt.token)
			rec := httptest.NewRecorder()
			s.handler.ServeHTTP(rec, req)
			var got actionsResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("response = %d %s (%v)", rec.Code, rec.Body, err)
			}
			if got != tt.want {
				t.Fatalf("actions = %+v, want %+v", got, tt.want)
			}
		})
	}
}
