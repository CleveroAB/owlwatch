package peers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CleveroAB/owlwatch/internal/metrics"
)

const waitTimeout = 5 * time.Second

// testPeer builds a Peer pointing at a test server URL.
func testPeer(t *testing.T, id, rawURL, token string) Peer {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parsing %q: %v", rawURL, err)
	}
	return Peer{ID: id, Name: id, URL: u, Token: token}
}

// newTestClient builds a Client with reconnect timing shrunk so tests run in
// milliseconds — the same knob-injection trick as the collector's
// usageTimeout.
func newTestClient(t *testing.T, ps ...Peer) *Client {
	t.Helper()
	c := NewClient(ps)
	c.baseBackoff = 10 * time.Millisecond
	c.maxBackoff = 40 * time.Millisecond
	c.historyTimeout = 2 * time.Second
	return c
}

// startClient runs c.Run in a goroutine and registers a cleanup that cancels
// it and waits for it to return.
func startClient(t *testing.T, c *Client) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(waitTimeout):
			t.Error("Run did not return after context cancel")
		}
	})
}

// startSSE writes the SSE response preamble and returns the flusher.
func startSSE(w http.ResponseWriter) http.Flusher {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fl := w.(http.Flusher)
	fl.Flush()
	return fl
}

// writeEvent writes one SSE event in the server's wire format and flushes.
// It runs on handler goroutines, so failures use t.Error, never t.Fatal.
func writeEvent(t *testing.T, w http.ResponseWriter, fl http.Flusher, event string, payload any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Errorf("marshal %s payload: %v", event, err)
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	fl.Flush()
}

// waitEvent receives one event or fails the test.
func waitEvent(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("event channel closed")
		}
		return ev
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for an event")
	}
	return Event{}
}

// expectOnline asserts that the next event is a status transition to the
// given state.
func expectOnline(t *testing.T, ch <-chan Event, id string, online bool) Event {
	t.Helper()
	ev := waitEvent(t, ch)
	if ev.ID != id || ev.Online == nil || *ev.Online != online || ev.Snapshot != nil {
		t.Fatalf("event = %+v, want Online=%v for %q", ev, online, id)
	}
	return ev
}

func TestHelloAndSnapshotFlow(t *testing.T) {
	hello := helloPayload{
		Host: metrics.HostInfo{Hostname: "peer-host", OS: "linux"},
		Recent: []metrics.Snapshot{
			{TS: 100, CPU: metrics.CPUMetrics{UsagePct: 10}},
			{TS: 200, CPU: metrics.CPUMetrics{UsagePct: 20}},
		},
		IntervalMs: 2000,
	}
	headers := make(chan http.Header, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/live" {
			http.NotFound(w, r)
			return
		}
		headers <- r.Header.Clone()
		fl := startSSE(w)
		writeEvent(t, w, fl, "hello", hello)
		io.WriteString(w, ": ping\n\n") // heartbeat comment must be ignored
		fl.Flush()
		writeEvent(t, w, fl, "snapshot", metrics.Snapshot{TS: 300, CPU: metrics.CPUMetrics{UsagePct: 30}})
		// A spartan but legal encoding: no space after the colons, CRLF line
		// endings. The parser must accept it.
		io.WriteString(w, "event:snapshot\r\ndata:{\"ts\":400}\r\n\r\n")
		fl.Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close) // after startClient's cleanup: stop the client first, then the server

	c := newTestClient(t, testPeer(t, "web1", srv.URL, "s3cret"))
	events, cancelSub := c.Subscribe()
	defer cancelSub()
	startClient(t, c)

	expectOnline(t, events, "web1", true)
	ev := waitEvent(t, events)
	if ev.ID != "web1" || ev.Snapshot == nil || ev.Snapshot.TS != 300 || ev.LastSeen == 0 {
		t.Fatalf("event = %+v, want snapshot TS 300 with LastSeen set", ev)
	}
	ev = waitEvent(t, events)
	if ev.Snapshot == nil || ev.Snapshot.TS != 400 {
		t.Fatalf("event = %+v, want snapshot TS 400 (no-space/CRLF encoding)", ev)
	}

	h := <-headers
	if got := h.Get("Authorization"); got != "Bearer s3cret" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer s3cret")
	}
	if got := h.Get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept header = %q, want %q", got, "text/event-stream")
	}

	// Cached state after hello + snapshots.
	hi, ok := c.HostInfo("web1")
	if !ok || hi.Hostname != "peer-host" {
		t.Errorf("HostInfo(web1) = (%+v, %v), want the hello host", hi, ok)
	}
	if got := c.IntervalMs("web1"); got != 2000 {
		t.Errorf("IntervalMs(web1) = %d, want 2000", got)
	}
	recent := c.Recent("web1")
	wantTS := []int64{100, 200, 300, 400} // hello seed + live snapshots, oldest first
	if len(recent) != len(wantTS) {
		t.Fatalf("Recent(web1) has %d snapshots, want %d", len(recent), len(wantTS))
	}
	for i, snap := range recent {
		if snap.TS != wantTS[i] {
			t.Errorf("Recent(web1)[%d].TS = %d, want %d", i, snap.TS, wantTS[i])
		}
	}
	servers := c.Servers()
	if len(servers) != 1 {
		t.Fatalf("Servers() returned %d entries, want 1", len(servers))
	}
	s := servers[0]
	if s.ID != "web1" || s.Local || !s.Online || s.IntervalMs != 2000 || s.LastSeen == 0 {
		t.Errorf("summary = %+v, want online web1 with interval 2000", s)
	}
	if s.Host == nil || s.Host.Hostname != "peer-host" {
		t.Errorf("summary.Host = %+v, want the hello host", s.Host)
	}
	if s.Latest == nil || s.Latest.TS != 400 {
		t.Errorf("summary.Latest = %+v, want TS 400", s.Latest)
	}
	// §9.4: the summary carries the ring's CPU values, oldest first, so
	// overview sparklines render on first paint. The raw TS-400 event
	// carried no cpu field, so its usage decodes to 0.
	wantCPU := []float64{10, 20, 30, 0}
	if len(s.RecentCPU) != len(wantCPU) {
		t.Fatalf("summary.RecentCPU = %v, want %v", s.RecentCPU, wantCPU)
	}
	for i, want := range wantCPU {
		if s.RecentCPU[i] != want {
			t.Errorf("summary.RecentCPU[%d] = %v, want %v", i, s.RecentCPU[i], want)
		}
	}

	// Unknown ids across all accessors.
	if _, ok := c.HostInfo("ghost"); ok {
		t.Error("HostInfo(ghost) ok = true, want false")
	}
	if got := c.Recent("ghost"); got != nil {
		t.Errorf("Recent(ghost) = %v, want nil", got)
	}
	if got := c.IntervalMs("ghost"); got != 0 {
		t.Errorf("IntervalMs(ghost) = %d, want 0", got)
	}
}

func TestRingSeedingCapped(t *testing.T) {
	recent := make([]metrics.Snapshot, ringCap+10)
	for i := range recent {
		recent[i].TS = int64(i + 1)             // 1..160
		recent[i].CPU.UsagePct = float64(i + 1) // mirrors TS: encodes the ring position
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl := startSSE(w)
		writeEvent(t, w, fl, "hello", helloPayload{Recent: recent, IntervalMs: 1000})
		writeEvent(t, w, fl, "snapshot", metrics.Snapshot{TS: 9999, CPU: metrics.CPUMetrics{UsagePct: 9999}})
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, testPeer(t, "web1", srv.URL, ""))
	events, cancelSub := c.Subscribe()
	defer cancelSub()
	startClient(t, c)

	expectOnline(t, events, "web1", true)
	waitEvent(t, events) // the snapshot: hello is fully applied before it

	got := c.Recent("web1")
	if len(got) != ringCap {
		t.Fatalf("Recent() has %d snapshots, want the cap %d", len(got), ringCap)
	}
	// Seeding keeps the newest 150 of 160 (11..160); the snapshot then
	// evicts the oldest → 12..160, 9999.
	if got[0].TS != 12 {
		t.Errorf("oldest TS = %d, want 12", got[0].TS)
	}
	if got[ringCap-1].TS != 9999 {
		t.Errorf("newest TS = %d, want 9999", got[ringCap-1].TS)
	}

	// §9.4: with 150 ring entries the summary's RecentCPU is downsampled to
	// 60 points, evenly spaced, oldest first, always keeping the newest.
	// The CPU values encode ring positions (12..160, then 9999).
	cpu := c.Servers()[0].RecentCPU
	if len(cpu) != recentCPUPoints {
		t.Fatalf("RecentCPU has %d points, want the cap %d", len(cpu), recentCPUPoints)
	}
	if cpu[0] != 12 {
		t.Errorf("RecentCPU[0] = %v, want 12 (the oldest ring entry)", cpu[0])
	}
	if cpu[len(cpu)-1] != 9999 {
		t.Errorf("RecentCPU[%d] = %v, want 9999 (the newest sample must survive downsampling)", len(cpu)-1, cpu[len(cpu)-1])
	}
	// Even spacing: 149 ring steps over 59 gaps → every pick advances the
	// ring by 2 or 3 slots. The last gap lands on 9999 and is skipped.
	for k := 0; k+2 < len(cpu); k++ {
		if gap := cpu[k+1] - cpu[k]; gap < 2 || gap > 3 {
			t.Errorf("RecentCPU gap [%d→%d] = %v, want 2 or 3 (even downsampling)", k, k+1, gap)
		}
	}
}

func TestReconnectAfterUpstreamClose(t *testing.T) {
	var mu sync.Mutex
	conns := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns++
		n := conns
		mu.Unlock()
		fl := startSSE(w)
		writeEvent(t, w, fl, "hello", helloPayload{Host: metrics.HostInfo{Hostname: "h"}, IntervalMs: 1000})
		if n == 1 {
			return // close the stream: the client must reconnect
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, testPeer(t, "web1", srv.URL, ""))
	events, cancelSub := c.Subscribe()
	defer cancelSub()
	startClient(t, c)

	expectOnline(t, events, "web1", true)
	expectOnline(t, events, "web1", false) // upstream closed → offline transition
	expectOnline(t, events, "web1", true)  // reconnected after backoff

	mu.Lock()
	n := conns
	mu.Unlock()
	if n < 2 {
		t.Errorf("peer saw %d connections, want at least 2", n)
	}
}

func TestStallTriggersReconnect(t *testing.T) {
	var mu sync.Mutex
	conns := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns++
		mu.Unlock()
		fl := startSSE(w)
		writeEvent(t, w, fl, "hello", helloPayload{IntervalMs: 1000})
		<-r.Context().Done() // then total silence: the watchdog must fire
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, testPeer(t, "web1", srv.URL, ""))
	c.stallTimeout = 80 * time.Millisecond
	events, cancelSub := c.Subscribe()
	defer cancelSub()
	startClient(t, c)

	expectOnline(t, events, "web1", true)
	expectOnline(t, events, "web1", false) // stalled → watchdog reconnects
	expectOnline(t, events, "web1", true)

	mu.Lock()
	n := conns
	mu.Unlock()
	if n < 2 {
		t.Errorf("peer saw %d connections, want at least 2 after a stall", n)
	}
}

func TestHeartbeatsResetStallTimer(t *testing.T) {
	var mu sync.Mutex
	conns := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns++
		mu.Unlock()
		fl := startSSE(w)
		writeEvent(t, w, fl, "hello", helloPayload{IntervalMs: 1000})
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				io.WriteString(w, ": ping\n\n")
				fl.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, testPeer(t, "web1", srv.URL, ""))
	c.stallTimeout = 300 * time.Millisecond
	events, cancelSub := c.Subscribe()
	defer cancelSub()
	startClient(t, c)

	expectOnline(t, events, "web1", true)

	// Heartbeat comments arrive well inside the stall window; the connection
	// must survive several windows without an offline transition.
	time.Sleep(900 * time.Millisecond)
	select {
	case ev := <-events:
		t.Fatalf("unexpected event %+v, heartbeats should keep the stream alive", ev)
	default:
	}
	mu.Lock()
	n := conns
	mu.Unlock()
	if n != 1 {
		t.Errorf("peer saw %d connections, want 1", n)
	}
	if !c.Servers()[0].Online {
		t.Error("peer reported offline despite heartbeats")
	}
}

func TestOversizedEventAbortsConnection(t *testing.T) {
	// A peer streaming endless data: lines (never a blank line) must not grow
	// hub memory without bound: readEvents drops the connection once the
	// accumulated payload exceeds maxEventBytes and reconnect takes over.
	var mu sync.Mutex
	conns := 0
	var flooded int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns++
		conn := conns
		mu.Unlock()
		fl := startSSE(w)
		if conn > 1 {
			// Reconnects get a healthy stream so the flood is measured once.
			writeEvent(t, w, fl, "hello", helloPayload{IntervalMs: 1000})
			<-r.Context().Done()
			return
		}
		chunk := "data: " + strings.Repeat("x", 64*1024) + "\n"
		for {
			n, err := io.WriteString(w, chunk)
			mu.Lock()
			flooded += int64(n)
			mu.Unlock()
			if err != nil {
				return // the client hung up — exactly what the cap demands
			}
			fl.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, testPeer(t, "web1", srv.URL, ""))
	events, cancelSub := c.Subscribe()
	defer cancelSub()
	startClient(t, c)

	expectOnline(t, events, "web1", true)
	expectOnline(t, events, "web1", false) // cap hit → connection aborted
	expectOnline(t, events, "web1", true)  // normal reconnect afterwards

	mu.Lock()
	got := flooded
	mu.Unlock()
	// The client reads ≈maxEventBytes before bailing; 2x covers what the
	// server managed to buffer before its write failed.
	if got > 2*maxEventBytes {
		t.Errorf("peer wrote %d bytes before the client disconnected, want ≈%d", got, maxEventBytes)
	}
}

func TestNonSSEContentTypeStaysOffline(t *testing.T) {
	// Any web service (or captive portal) answers 200 text/html; treating
	// that as a healthy connect would broadcast Online:true and reset the
	// backoff to base, flapping forever at 2s intervals.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<html>captive portal</html>")
	}))
	t.Cleanup(srv.Close)
	p := testPeer(t, "web1", srv.URL, "")
	c := newTestClient(t, p)

	// connected=false is what lets the backoff grow instead of resetting to
	// base on every attempt.
	if c.stream(context.Background(), p) {
		t.Error("stream() reported a healthy connect for a text/html response")
	}

	events, cancelSub := c.Subscribe()
	defer cancelSub()
	startClient(t, c)
	select {
	case ev := <-events:
		t.Fatalf("unexpected event %+v from a non-SSE peer", ev)
	case <-time.After(150 * time.Millisecond):
	}
	if c.Servers()[0].Online {
		t.Error("peer came online through a text/html response")
	}
}

func TestRedirectRefused(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
	}))
	defer target.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirecting.Close()

	c := newTestClient(t, testPeer(t, "web1", redirecting.URL, "tok"))

	if _, err := c.History(context.Background(), "web1", "1h"); !errors.Is(err, ErrPeerUnavailable) {
		t.Errorf("History through a redirect: err = %v, want ErrPeerUnavailable", err)
	}

	events, cancelSub := c.Subscribe()
	defer cancelSub()
	startClient(t, c)
	select {
	case ev := <-events:
		t.Fatalf("unexpected event %+v from a redirecting peer", ev)
	case <-time.After(150 * time.Millisecond):
	}
	if c.Servers()[0].Online {
		t.Error("peer came online through a redirect")
	}
	if n := targetHits.Load(); n != 0 {
		t.Errorf("redirect target was hit %d times, want 0", n)
	}
}

func TestHistory(t *testing.T) {
	points := []metrics.HistoryPoint{
		{TS: 1000, CPUPct: 12.5, Disks: map[string]float64{"/": 40}},
		{TS: 2000, CPUPct: 50, Disks: map[string]float64{}},
	}
	var mu sync.Mutex
	var gotRange, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/history" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		gotRange = r.URL.Query().Get("range")
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"range": "6h", "points": points})
	}))
	defer srv.Close()

	c := newTestClient(t, testPeer(t, "db1", srv.URL, "tok"))
	got, err := c.History(context.Background(), "db1", "6h")
	if err != nil {
		t.Fatalf("History() error: %v", err)
	}
	if len(got) != 2 || got[0].TS != 1000 || got[0].CPUPct != 12.5 || got[1].TS != 2000 {
		t.Errorf("History() = %+v, want the served points", got)
	}
	if got[0].Disks["/"] != 40 {
		t.Errorf("History()[0].Disks = %v, want {\"/\": 40}", got[0].Disks)
	}
	mu.Lock()
	if gotRange != "6h" {
		t.Errorf("peer received range %q, want %q", gotRange, "6h")
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("peer received Authorization %q, want %q", gotAuth, "Bearer tok")
	}
	mu.Unlock()

	_, err = c.History(context.Background(), "ghost", "6h")
	if !errors.Is(err, ErrUnknownPeer) {
		t.Errorf("History(ghost) error = %v, want ErrUnknownPeer", err)
	}
	if errors.Is(err, ErrPeerUnavailable) {
		t.Errorf("History(ghost) error = %v must not match ErrPeerUnavailable", err)
	}
}

func TestHistoryPeerFailures(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done() // never answer; the client's deadline must fire
		}))
		defer srv.Close()
		c := newTestClient(t, testPeer(t, "db1", srv.URL, ""))
		c.historyTimeout = 50 * time.Millisecond
		start := time.Now()
		_, err := c.History(context.Background(), "db1", "1h")
		if !errors.Is(err, ErrPeerUnavailable) {
			t.Errorf("error = %v, want ErrPeerUnavailable", err)
		}
		if elapsed := time.Since(start); elapsed > waitTimeout {
			t.Errorf("History() took %v, the 50ms timeout did not bound it", elapsed)
		}
	})
	t.Run("http error status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
		}))
		defer srv.Close()
		c := newTestClient(t, testPeer(t, "db1", srv.URL, ""))
		if _, err := c.History(context.Background(), "db1", "1h"); !errors.Is(err, ErrPeerUnavailable) {
			t.Errorf("error = %v, want ErrPeerUnavailable", err)
		}
	})
	t.Run("connection refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		srv.Close() // closed immediately: nothing listens there anymore
		c := newTestClient(t, testPeer(t, "db1", srv.URL, ""))
		if _, err := c.History(context.Background(), "db1", "1h"); !errors.Is(err, ErrPeerUnavailable) {
			t.Errorf("error = %v, want ErrPeerUnavailable", err)
		}
	})
	t.Run("endless body is capped", func(t *testing.T) {
		// A peer streaming a never-ending JSON array must fail within the
		// maxHistoryBytes limit, not allocate unboundedly (or spin until the
		// history timeout).
		var written atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fl := w.(http.Flusher)
			io.WriteString(w, `{"points":[`)
			chunk := strings.Repeat(`{"ts":1,"cpuPct":50},`, 4096)
			for {
				n, err := io.WriteString(w, chunk)
				written.Add(int64(n))
				if err != nil {
					return // the client stopped reading at the cap
				}
				fl.Flush()
			}
		}))
		t.Cleanup(srv.Close)
		c := newTestClient(t, testPeer(t, "db1", srv.URL, ""))
		if _, err := c.History(context.Background(), "db1", "1h"); !errors.Is(err, ErrPeerUnavailable) {
			t.Errorf("error = %v, want ErrPeerUnavailable", err)
		}
		// 2x covers what the server managed to buffer before its write failed.
		if got := written.Load(); got > 2*maxHistoryBytes {
			t.Errorf("peer wrote %d bytes before History failed, want ≈%d", got, maxHistoryBytes)
		}
	})
	t.Run("null points become an empty slice", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"range":"1h","points":null}`)
		}))
		defer srv.Close()
		c := newTestClient(t, testPeer(t, "db1", srv.URL, ""))
		got, err := c.History(context.Background(), "db1", "1h")
		if err != nil {
			t.Fatalf("History() error: %v", err)
		}
		if got == nil || len(got) != 0 {
			t.Errorf("History() = %v, want a non-nil empty slice", got)
		}
	})
}

func TestServersBeforeAnyConnection(t *testing.T) {
	c := NewClient([]Peer{
		testPeer(t, "b", "http://127.0.0.1:1", ""),
		testPeer(t, "a", "http://127.0.0.1:1", ""),
	})
	servers := c.Servers()
	if len(servers) != 2 || servers[0].ID != "b" || servers[1].ID != "a" {
		t.Fatalf("Servers() = %+v, want b then a (configured order)", servers)
	}
	for _, s := range servers {
		if s.Online || s.Local || s.LastSeen != 0 || s.IntervalMs != 0 || s.Host != nil || s.Latest != nil || s.RecentCPU != nil {
			t.Errorf("summary %+v, want offline/empty before any connection", s)
		}
	}
	// Known peer, hello never seen: ok=true with the zero HostInfo.
	if hi, ok := c.HostInfo("a"); !ok || hi.Hostname != "" {
		t.Errorf("HostInfo(a) = (%+v, %v), want the zero HostInfo with ok=true", hi, ok)
	}
	if got := c.Recent("a"); got == nil || len(got) != 0 {
		t.Errorf("Recent(a) = %v, want a non-nil empty slice", got)
	}
}

func TestSubscribeDropsWithoutBlocking(t *testing.T) {
	p := testPeer(t, "web1", "http://127.0.0.1:1", "")
	c := NewClient([]Peer{p})
	ch, cancel := c.Subscribe()
	defer cancel()

	// Broadcast far more than the subscriber buffer without reading; the
	// broadcast must never block.
	total := subscriberBuffer + 10
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ts := 1; ts <= total; ts++ {
			c.applySnapshot(p, metrics.Snapshot{TS: int64(ts)})
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast blocked on a slow subscriber")
	}

	// The subscriber sees exactly the first subscriberBuffer events; the
	// rest were dropped.
	for want := int64(1); want <= subscriberBuffer; want++ {
		ev := <-ch
		if ev.Snapshot == nil || ev.Snapshot.TS != want {
			t.Fatalf("received %+v, want snapshot TS %d", ev, want)
		}
	}
	select {
	case ev := <-ch:
		t.Fatalf("received extra event %+v, want none", ev)
	default:
	}
}

func TestSubscribeCancelIsIdempotent(t *testing.T) {
	p := testPeer(t, "web1", "http://127.0.0.1:1", "")
	c := NewClient([]Peer{p})
	ch, cancel := c.Subscribe()

	cancel()
	cancel() // second call must be a no-op, not a double-close panic

	if _, ok := <-ch; ok {
		t.Error("channel still open after cancel")
	}
	c.applySnapshot(p, metrics.Snapshot{TS: 1}) // must not panic on the removed sub
}

func TestRunClosesSubscribersOnCancel(t *testing.T) {
	c := NewClient(nil) // even with zero peers Run blocks until cancelled
	ch, _ := c.Subscribe()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(waitTimeout):
		t.Fatal("Run did not return on context cancel")
	}
	if _, ok := <-ch; ok {
		t.Error("subscriber channel still open after Run returned")
	}
	ch2, cancel2 := c.Subscribe()
	if _, ok := <-ch2; ok {
		t.Error("Subscribe after Run returned an open channel")
	}
	cancel2()
}

func TestJitterWithinBounds(t *testing.T) {
	d := 2 * time.Second
	for i := 0; i < 1000; i++ {
		if j := jitter(d); j < d/2 || j >= d {
			t.Fatalf("jitter(%v) = %v, want within [%v, %v)", d, j, d/2, d)
		}
	}
	if got := jitter(1); got != 1 {
		t.Errorf("jitter(1ns) = %v, want the degenerate duration back", got)
	}
}
