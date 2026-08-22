package server

import (
	"context"
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

func rebootRequest(t *testing.T, s *Server, path string, confirmed bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Host = "127.0.0.1:8080"
	if confirmed {
		req.Header.Set(rebootActionHeader, "reboot")
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func newRebootServer(reboot func()) *Server {
	col := collector.New(collector.Config{SampleInterval: time.Second})
	return New(Config{
		Collector:      col,
		Host:           col.HostInfo(),
		SampleInterval: time.Second,
		Reboot:         reboot,
	})
}

func TestRebootRequiresConfirmationHeader(t *testing.T) {
	var calls atomic.Int32
	rec := rebootRequest(t, newRebootServer(func() { calls.Add(1) }), "/api/reboot", false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	time.Sleep(150 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("reboot calls = %d, want 0", calls.Load())
	}
}

func TestRebootUnavailable(t *testing.T) {
	rec := rebootRequest(t, newRebootServer(nil), "/api/reboot", true)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "unavailable") {
		t.Fatalf("response = %d %s, want 503 unavailable", rec.Code, rec.Body)
	}
}

func TestRebootRequiresConfiguredToken(t *testing.T) {
	var calls atomic.Int32
	col := collector.New(collector.Config{SampleInterval: time.Second})
	s := New(Config{
		Collector:      col,
		SampleInterval: time.Second,
		Token:          "0123456789abcdef",
		Reboot:         func() { calls.Add(1) },
	})
	rec := rebootRequest(t, s, "/api/reboot", true)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	time.Sleep(150 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("reboot calls = %d, want 0", calls.Load())
	}
}

func TestRebootAcceptedExactlyOnce(t *testing.T) {
	calls := make(chan struct{}, 2)
	s := newRebootServer(func() { calls <- struct{}{} })
	for range 2 {
		rec := rebootRequest(t, s, "/api/servers/local/reboot", true)
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
	s := newFederationServer(t, fleet)
	rec := rebootRequest(t, s, "/api/servers/web1/reboot", true)
	if rec.Code != http.StatusAccepted || called != "web1" {
		t.Fatalf("response = %d, called = %q; want 202 and web1", rec.Code, called)
	}
}

func TestServerRebootMapsPeerFailures(t *testing.T) {
	fleet := &fakeFleet{
		servers: []metrics.ServerSummary{{ID: "web1", Name: "Web One", Online: true}},
		reboot: func(context.Context, string) error {
			return peers.ErrPeerUnavailable
		},
	}
	s := newFederationServer(t, fleet)
	rec := rebootRequest(t, s, "/api/servers/web1/reboot", true)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}

	rec = rebootRequest(t, s, "/api/servers/ghost/reboot", true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d, want 404", rec.Code)
	}
}
