package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CleveroAB/owlwatch/internal/collector"
	"github.com/CleveroAB/owlwatch/internal/metrics"
	"github.com/CleveroAB/owlwatch/internal/peers"
)

// fakeFleet implements peerSource so the federation handlers can be tested
// without live peer connections. Config.Peers stays the concrete
// *peers.Client per the DESIGN.md §9.4 contract, so tests inject the fake
// directly into Server.peers (same package).
type fakeFleet struct {
	mu      sync.Mutex
	servers []metrics.ServerSummary
	recent  map[string][]metrics.Snapshot
	history func(ctx context.Context, id, rangeKey string) ([]metrics.HistoryPoint, error)
	events  chan peers.Event
}

func (f *fakeFleet) Servers() []metrics.ServerSummary {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]metrics.ServerSummary, len(f.servers))
	copy(out, f.servers)
	return out
}

func (f *fakeFleet) Subscribe() (<-chan peers.Event, func()) {
	return f.events, func() {}
}

func (f *fakeFleet) Recent(id string) []metrics.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recent[id]
}

func (f *fakeFleet) History(ctx context.Context, id, rangeKey string) ([]metrics.HistoryPoint, error) {
	return f.history(ctx, id, rangeKey)
}

// setHost marks a peer's hello as cached, as the peers client does when the
// upstream hello arrives.
func (f *fakeFleet) setHost(id string, host metrics.HostInfo, intervalMs int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.servers {
		if f.servers[i].ID == id {
			f.servers[i].Host = &host
			f.servers[i].IntervalMs = intervalMs
			f.servers[i].Online = true
		}
	}
}

// setOnline updates a peer's cached reachability, as the peers client does
// before broadcasting a transition.
func (f *fakeFleet) setOnline(id string, online bool, lastSeen int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.servers {
		if f.servers[i].ID == id {
			f.servers[i].Online = online
			f.servers[i].LastSeen = lastSeen
		}
	}
}

// newFederationServer builds a Server whose peer state is the fake fleet.
func newFederationServer(t *testing.T, fleet *fakeFleet) *Server {
	t.Helper()
	col := collector.New(collector.Config{SampleInterval: 250 * time.Millisecond})
	s := New(Config{
		Collector:      col,
		Host:           col.HostInfo(),
		SampleInterval: 250 * time.Millisecond,
	})
	if fleet != nil {
		s.peers = fleet
	}
	return s
}

func get(t *testing.T, s *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = "127.0.0.1:8080"
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func snapAt(ts int64) metrics.Snapshot {
	return metrics.Snapshot{TS: ts, Disks: []metrics.DiskMetrics{}, GPUs: []metrics.GPUMetrics{}}
}

// Standalone (nil Peers): /api/servers reports exactly one local entry, so
// the UI can keep rendering the pixel-identical v1 dashboard (DESIGN.md §9.5).
// The local entry carries recentCpu from the collector ring (§9.4).
func TestHandleServersStandalone(t *testing.T) {
	col := collector.New(collector.Config{SampleInterval: 250 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go col.Run(ctx)
	// Run samples immediately; wait for the first one so the local entry
	// carries Latest and RecentCPU.
	for deadline := time.Now().Add(10 * time.Second); ; {
		if _, ok := col.Latest(); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("collector produced no sample")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s := New(Config{
		Collector:      col,
		Host:           col.HostInfo(),
		SampleInterval: 250 * time.Millisecond,
	})

	rec := get(t, s, "/api/servers")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var servers []metrics.ServerSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &servers); err != nil {
		t.Fatalf("unmarshal servers: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("len(servers) = %d, want exactly 1 (standalone invariant)", len(servers))
	}
	local := servers[0]
	if local.ID != "local" || !local.Local || !local.Online {
		t.Errorf("local entry = %+v, want id=local local=true online=true", local)
	}
	if local.IntervalMs != 250 {
		t.Errorf("local intervalMs = %d, want 250", local.IntervalMs)
	}
	if local.Host == nil {
		t.Error("local host is nil, want the collector's HostInfo")
	}
	if len(local.RecentCPU) == 0 || len(local.RecentCPU) > recentCPUPoints {
		t.Errorf("local recentCpu has %d points, want 1..%d from the collector ring",
			len(local.RecentCPU), recentCPUPoints)
	}
}

// recentCPU: short rings pass through unchanged; longer rings downsample
// evenly to 60 points, oldest first, keeping the newest sample (§9.4).
func TestRecentCPUDownsample(t *testing.T) {
	if got := recentCPU(nil); got != nil {
		t.Fatalf("recentCPU(nil) = %v, want nil", got)
	}

	cpuSnap := func(pct float64) metrics.Snapshot {
		return metrics.Snapshot{CPU: metrics.CPUMetrics{UsagePct: pct}}
	}
	short := recentCPU([]metrics.Snapshot{cpuSnap(1), cpuSnap(2), cpuSnap(3)})
	if len(short) != 3 || short[0] != 1 || short[2] != 3 {
		t.Fatalf("short ring = %v, want [1 2 3] unchanged", short)
	}

	long := make([]metrics.Snapshot, 150) // full peers ring
	for i := range long {
		long[i] = cpuSnap(float64(i))
	}
	got := recentCPU(long)
	if len(got) != recentCPUPoints {
		t.Fatalf("len = %d, want %d", len(got), recentCPUPoints)
	}
	if got[0] != 0 || got[len(got)-1] != 149 {
		t.Errorf("endpoints = %v, %v; want the oldest (0) and newest (149) samples kept",
			got[0], got[len(got)-1])
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("points not evenly increasing at %d: %v", i, got)
		}
	}
}

// A hub lists local first, then the peers in configured order (§9.4). Peer
// recentCpu comes straight from Peers.Servers() — pass-through, not
// re-derived.
func TestHandleServersHubOrder(t *testing.T) {
	fleet := &fakeFleet{servers: []metrics.ServerSummary{
		{ID: "web1", Name: "web1", Online: true, RecentCPU: []float64{1, 2, 3}},
		{ID: "db1", Name: "db1"},
	}}
	s := newFederationServer(t, fleet)

	rec := get(t, s, "/api/servers")
	var servers []metrics.ServerSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &servers); err != nil {
		t.Fatalf("unmarshal servers: %v", err)
	}
	var ids []string
	for _, sum := range servers {
		ids = append(ids, sum.ID)
	}
	if got, want := strings.Join(ids, ","), "local,web1,db1"; got != want {
		t.Fatalf("server ids = %q, want %q", got, want)
	}
	if got := servers[1].RecentCPU; len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("web1 recentCpu = %v, want the fleet's [1 2 3] passed through", got)
	}
}

// Unknown ids answer 404 {"error":"unknown server"} on every /api/servers/{id}/*
// route (§9.6), standalone and hub alike.
func TestServerRoutesUnknownID(t *testing.T) {
	fleet := &fakeFleet{servers: []metrics.ServerSummary{{ID: "web1", Name: "web1"}}}
	for _, mode := range []struct {
		name  string
		fleet *fakeFleet
	}{{"standalone", nil}, {"hub", fleet}} {
		s := newFederationServer(t, mode.fleet)
		for _, path := range []string{
			"/api/servers/nope/host",
			"/api/servers/nope/history?range=1h",
			"/api/servers/nope/live",
		} {
			rec := get(t, s, path)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s: status = %d, want %d", mode.name, path, rec.Code, http.StatusNotFound)
			}
			if body := strings.TrimSpace(rec.Body.String()); body != `{"error":"unknown server"}` {
				t.Errorf("%s %s: body = %q, want unknown-server JSON", mode.name, path, body)
			}
		}
	}
}

// /api/servers/local/* must behave exactly like the legacy local routes.
func TestHandleServerHostLocalAlias(t *testing.T) {
	s := newFederationServer(t, nil)

	legacy := get(t, s, "/api/host")
	scoped := get(t, s, "/api/servers/local/host")
	if scoped.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", scoped.Code, http.StatusOK)
	}
	if legacy.Body.String() != scoped.Body.String() {
		t.Errorf("/api/servers/local/host = %q, want the /api/host body %q",
			scoped.Body.String(), legacy.Body.String())
	}
}

// A configured peer whose hello has never been cached answers 502 on /host;
// once cached, /host serves the cached HostInfo (§9.6).
func TestHandleServerHostPeer(t *testing.T) {
	fleet := &fakeFleet{servers: []metrics.ServerSummary{{ID: "web1", Name: "web1"}}}
	s := newFederationServer(t, fleet)

	rec := get(t, s, "/api/servers/web1/host")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("never-seen peer: status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"error":"peer unreachable"}` {
		t.Fatalf("never-seen peer: body = %q, want peer-unreachable JSON", body)
	}

	fleet.setHost("web1", metrics.HostInfo{Hostname: "web1.example", GPUNames: []string{}}, 2000)
	rec = get(t, s, "/api/servers/web1/host")
	if rec.Code != http.StatusOK {
		t.Fatalf("cached peer: status = %d, want %d", rec.Code, http.StatusOK)
	}
	var host metrics.HostInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &host); err != nil {
		t.Fatalf("unmarshal host: %v", err)
	}
	if host.Hostname != "web1.example" {
		t.Errorf("hostname = %q, want %q", host.Hostname, "web1.example")
	}
}

// Peer history proxying: 400 on a bad range without hitting the peer, the
// §9.6 sentinel-to-status mapping, and points passed through never-null.
func TestHandleServerHistoryPeer(t *testing.T) {
	var gotID, gotRange string
	historyErr := error(nil)
	var historyPoints []metrics.HistoryPoint
	fleet := &fakeFleet{
		servers: []metrics.ServerSummary{{ID: "web1", Name: "web1"}},
		history: func(ctx context.Context, id, rangeKey string) ([]metrics.HistoryPoint, error) {
			gotID, gotRange = id, rangeKey
			return historyPoints, historyErr
		},
	}
	s := newFederationServer(t, fleet)

	rec := get(t, s, "/api/servers/web1/history?range=99y")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad range: status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if gotRange != "" {
		t.Errorf("bad range: proxied to peer with range %q, want no proxy call", gotRange)
	}

	historyErr = peers.ErrPeerUnavailable
	rec = get(t, s, "/api/servers/web1/history?range=1h")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("unavailable peer: status = %d, want %d", rec.Code, http.StatusBadGateway)
	}

	historyErr = peers.ErrUnknownPeer // defensive: the client is authoritative
	rec = get(t, s, "/api/servers/web1/history?range=1h")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown-peer sentinel: status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	historyErr = nil
	historyPoints = nil // a nil proxy result must still marshal as []
	rec = get(t, s, "/api/servers/web1/history?range=6h")
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy success: status = %d, want %d", rec.Code, http.StatusOK)
	}
	if gotID != "web1" || gotRange != "6h" {
		t.Errorf("proxied id/range = %q/%q, want web1/6h", gotID, gotRange)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"range":"6h","points":[]}` {
		t.Errorf("body = %q, want empty points array, never null", body)
	}
}

// sseEvent is one parsed server-sent event.
type sseEvent struct {
	name string
	data string
}

// readEvents streams SSE from target on a real server (the recorder cannot
// stream) and returns the first n events, skipping heartbeat comments.
func readEvents(t *testing.T, s *Server, target string, n int, during func()) []sseEvent {
	t.Helper()
	srv := httptest.NewServer(s.handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+target, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if during != nil {
		during()
	}

	var (
		events []sseEvent
		cur    sseEvent
	)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, ":"):
			// heartbeat comment
		case line == "":
			if cur.name != "" || cur.data != "" {
				events = append(events, cur)
				cur = sseEvent{}
				if len(events) == n {
					return events
				}
			}
		default:
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				cur.name = v
			}
			if v, ok := strings.CutPrefix(line, "data: "); ok {
				cur.data = v
			}
		}
	}
	t.Fatalf("stream ended after %d of %d events: %v", len(events), n, scanner.Err())
	return nil
}

// A peer whose hello is cached gets the exact v1 stream: hello (cached host,
// ring, intervalMs) first, then snapshots relayed from the shared
// subscription filtered to that id (§9.4).
func TestHandleServerLivePeerCachedHello(t *testing.T) {
	host := metrics.HostInfo{Hostname: "web1.example", GPUNames: []string{}}
	fleet := &fakeFleet{
		servers: []metrics.ServerSummary{
			{ID: "web1", Name: "web1", Online: true, Host: &host, IntervalMs: 2000},
			{ID: "db1", Name: "db1", Online: true, Host: &host, IntervalMs: 2000},
		},
		recent: map[string][]metrics.Snapshot{"web1": {snapAt(1), snapAt(2)}},
		events: make(chan peers.Event, 4),
	}
	s := newFederationServer(t, fleet)

	otherSnap := snapAt(3)
	webSnap := snapAt(4)
	events := readEvents(t, s, "/api/servers/web1/live", 2, func() {
		fleet.events <- peers.Event{ID: "db1", Snapshot: &otherSnap} // must be filtered out
		fleet.events <- peers.Event{ID: "web1", Snapshot: &webSnap}
	})

	if events[0].name != "hello" {
		t.Fatalf("first event = %q, want hello", events[0].name)
	}
	var hello struct {
		Host       metrics.HostInfo   `json:"host"`
		Recent     []metrics.Snapshot `json:"recent"`
		IntervalMs int64              `json:"intervalMs"`
	}
	if err := json.Unmarshal([]byte(events[0].data), &hello); err != nil {
		t.Fatalf("unmarshal hello: %v", err)
	}
	if hello.Host.Hostname != "web1.example" || len(hello.Recent) != 2 || hello.IntervalMs != 2000 {
		t.Fatalf("hello = host %q, %d recent, intervalMs %d; want web1.example, 2, 2000",
			hello.Host.Hostname, len(hello.Recent), hello.IntervalMs)
	}

	if events[1].name != "snapshot" {
		t.Fatalf("second event = %q, want snapshot", events[1].name)
	}
	var snap metrics.Snapshot
	if err := json.Unmarshal([]byte(events[1].data), &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snap.TS != 4 {
		t.Fatalf("snapshot ts = %d, want 4 (db1's snapshot must not leak in)", snap.TS)
	}
}

// A configured-but-never-seen peer first gets a status offline event; the
// hello follows once the peer's hello lands in the cache, and the online
// transition that revealed it is forwarded too (§9.4, §9.6).
func TestHandleServerLivePeerNotYetSeen(t *testing.T) {
	fleet := &fakeFleet{
		servers: []metrics.ServerSummary{{ID: "web1", Name: "web1"}},
		recent:  map[string][]metrics.Snapshot{},
		events:  make(chan peers.Event, 2),
	}
	s := newFederationServer(t, fleet)

	events := readEvents(t, s, "/api/servers/web1/live", 3, func() {
		// The peer connects: the peers client caches the hello, then
		// broadcasts the transition.
		fleet.setHost("web1", metrics.HostInfo{Hostname: "web1.example", GPUNames: []string{}}, 2000)
		online := true
		fleet.events <- peers.Event{ID: "web1", Online: &online, LastSeen: 42}
	})

	if events[0].name != "status" {
		t.Fatalf("first event = %q, want status", events[0].name)
	}
	var status struct {
		ID     string `json:"id"`
		Online bool   `json:"online"`
	}
	if err := json.Unmarshal([]byte(events[0].data), &status); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if status.ID != "web1" || status.Online {
		t.Fatalf("status = %+v, want id=web1 online=false", status)
	}
	if events[1].name != "hello" {
		t.Fatalf("second event = %q, want the deferred hello", events[1].name)
	}
	if events[2].name != "status" {
		t.Fatalf("third event = %q, want the forwarded online transition", events[2].name)
	}
	if err := json.Unmarshal([]byte(events[2].data), &status); err != nil {
		t.Fatalf("unmarshal online status: %v", err)
	}
	if status.ID != "web1" || !status.Online {
		t.Fatalf("online status = %+v, want id=web1 online=true", status)
	}
}

// A viewer connecting while a previously-seen peer is offline gets the cached
// hello followed immediately by status offline — a dashboard must never show
// a dead peer as live (§9.4).
func TestHandleServerLivePeerOfflineCachedHello(t *testing.T) {
	host := metrics.HostInfo{Hostname: "web1.example", GPUNames: []string{}}
	fleet := &fakeFleet{
		servers: []metrics.ServerSummary{
			{ID: "web1", Name: "web1", Online: false, LastSeen: 42, Host: &host, IntervalMs: 2000},
		},
		recent: map[string][]metrics.Snapshot{"web1": {snapAt(1)}},
		events: make(chan peers.Event),
	}
	s := newFederationServer(t, fleet)

	events := readEvents(t, s, "/api/servers/web1/live", 2, nil)

	if events[0].name != "hello" {
		t.Fatalf("first event = %q, want the cached hello", events[0].name)
	}
	if events[1].name != "status" {
		t.Fatalf("second event = %q, want status offline", events[1].name)
	}
	var status struct {
		ID       string `json:"id"`
		Online   bool   `json:"online"`
		LastSeen int64  `json:"lastSeen"`
	}
	if err := json.Unmarshal([]byte(events[1].data), &status); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if status.ID != "web1" || status.Online || status.LastSeen != 42 {
		t.Fatalf("status = %+v, want id=web1 online=false lastSeen=42", status)
	}
}

// Upstream reachability transitions are forwarded mid-stream as status
// events, and snapshots keep flowing after the peer recovers (§9.4).
func TestHandleServerLivePeerStatusForwarded(t *testing.T) {
	host := metrics.HostInfo{Hostname: "web1.example", GPUNames: []string{}}
	fleet := &fakeFleet{
		servers: []metrics.ServerSummary{
			{ID: "web1", Name: "web1", Online: true, Host: &host, IntervalMs: 2000},
		},
		recent: map[string][]metrics.Snapshot{},
		events: make(chan peers.Event, 4),
	}
	s := newFederationServer(t, fleet)

	recoverySnap := snapAt(11)
	events := readEvents(t, s, "/api/servers/web1/live", 4, func() {
		offline, online := false, true
		fleet.events <- peers.Event{ID: "web1", Online: &offline, LastSeen: 9}
		fleet.events <- peers.Event{ID: "web1", Online: &online, LastSeen: 10}
		fleet.events <- peers.Event{ID: "web1", Snapshot: &recoverySnap}
	})

	if events[0].name != "hello" {
		t.Fatalf("first event = %q, want hello", events[0].name)
	}
	var status struct {
		Online   bool  `json:"online"`
		LastSeen int64 `json:"lastSeen"`
	}
	if events[1].name != "status" {
		t.Fatalf("second event = %q, want the forwarded offline transition", events[1].name)
	}
	if err := json.Unmarshal([]byte(events[1].data), &status); err != nil {
		t.Fatalf("unmarshal offline status: %v", err)
	}
	if status.Online || status.LastSeen != 9 {
		t.Fatalf("offline status = %+v, want online=false lastSeen=9", status)
	}
	if events[2].name != "status" {
		t.Fatalf("third event = %q, want the forwarded online transition", events[2].name)
	}
	if err := json.Unmarshal([]byte(events[2].data), &status); err != nil {
		t.Fatalf("unmarshal online status: %v", err)
	}
	if !status.Online || status.LastSeen != 10 {
		t.Fatalf("online status = %+v, want online=true lastSeen=10", status)
	}
	if events[3].name != "snapshot" {
		t.Fatalf("fourth event = %q, want snapshots to flow after recovery", events[3].name)
	}
	var snap metrics.Snapshot
	if err := json.Unmarshal([]byte(events[3].data), &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snap.TS != 11 {
		t.Fatalf("snapshot ts = %d, want 11", snap.TS)
	}
}

// The overview stream opens with the full fleet state, then muxes peer
// snapshots and status transitions; every transition also re-sends the full
// servers event so dropped status events heal (§9.4).
func TestHandleOverviewLive(t *testing.T) {
	fleet := &fakeFleet{
		servers: []metrics.ServerSummary{{ID: "web1", Name: "web1", Online: true}},
		events:  make(chan peers.Event, 4),
	}
	s := newFederationServer(t, fleet)

	webSnap := snapAt(7)
	offline := false
	events := readEvents(t, s, "/api/overview/live", 4, func() {
		fleet.events <- peers.Event{ID: "web1", Snapshot: &webSnap}
		// The peers client updates its cache before broadcasting, so the
		// resync must observe the post-transition state.
		fleet.setOnline("web1", false, 99)
		fleet.events <- peers.Event{ID: "web1", Online: &offline, LastSeen: 99}
	})

	if events[0].name != "servers" {
		t.Fatalf("first event = %q, want servers", events[0].name)
	}
	var servers []metrics.ServerSummary
	if err := json.Unmarshal([]byte(events[0].data), &servers); err != nil {
		t.Fatalf("unmarshal servers: %v", err)
	}
	if len(servers) != 2 || servers[0].ID != "local" || servers[1].ID != "web1" {
		t.Fatalf("servers = %+v, want [local web1]", servers)
	}

	if events[1].name != "snapshot" {
		t.Fatalf("second event = %q, want snapshot", events[1].name)
	}
	var snapEv struct {
		ID       string           `json:"id"`
		Snapshot metrics.Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(events[1].data), &snapEv); err != nil {
		t.Fatalf("unmarshal snapshot event: %v", err)
	}
	if snapEv.ID != "web1" || snapEv.Snapshot.TS != 7 {
		t.Fatalf("snapshot event = %+v, want id=web1 ts=7", snapEv)
	}

	if events[2].name != "status" {
		t.Fatalf("third event = %q, want status", events[2].name)
	}
	var statusEv struct {
		ID       string `json:"id"`
		Online   bool   `json:"online"`
		LastSeen int64  `json:"lastSeen"`
	}
	if err := json.Unmarshal([]byte(events[2].data), &statusEv); err != nil {
		t.Fatalf("unmarshal status event: %v", err)
	}
	if statusEv.ID != "web1" || statusEv.Online || statusEv.LastSeen != 99 {
		t.Fatalf("status event = %+v, want id=web1 online=false lastSeen=99", statusEv)
	}

	if events[3].name != "servers" {
		t.Fatalf("fourth event = %q, want the transition-triggered servers resync", events[3].name)
	}
	if err := json.Unmarshal([]byte(events[3].data), &servers); err != nil {
		t.Fatalf("unmarshal resynced servers: %v", err)
	}
	if len(servers) != 2 || servers[1].ID != "web1" || servers[1].Online || servers[1].LastSeen != 99 {
		t.Fatalf("resynced servers = %+v, want web1 offline with lastSeen=99", servers)
	}
}

// The overview stream re-sends the full servers event on every heartbeat
// tick, so a status event dropped by the non-blocking broadcast — or a peer
// hello arriving after connect — heals within one cycle (§9.4).
func TestHandleOverviewLiveHeartbeatResync(t *testing.T) {
	orig := heartbeatInterval
	heartbeatInterval = 40 * time.Millisecond
	defer func() { heartbeatInterval = orig }()

	fleet := &fakeFleet{
		servers: []metrics.ServerSummary{{ID: "web1", Name: "web1", Online: true}},
		events:  make(chan peers.Event),
	}
	s := newFederationServer(t, fleet)

	// Nothing is pushed on the fleet channel and the collector is not
	// running, so a second servers event can only be the heartbeat resync.
	events := readEvents(t, s, "/api/overview/live", 2, nil)
	for i, ev := range events {
		if ev.name != "servers" {
			t.Fatalf("event[%d] = %q, want servers (connect, then heartbeat resync)", i, ev.name)
		}
	}
}

// A hub listed in its own OWLWATCH_PEERS (e.g. via its public domain) must
// not show the same machine twice: when a peer's HostInfo matches the local
// hostname + boot time, the implicit "local" entry is omitted and the
// operator-named peer represents the machine.
func TestHandleServersLocalAliasedByPeer(t *testing.T) {
	col := collector.New(collector.Config{SampleInterval: 250 * time.Millisecond})
	host := col.HostInfo()

	self := host // same hostname + boot time = same machine
	fleet := &fakeFleet{servers: []metrics.ServerSummary{
		{ID: "malevik", Name: "malevik", Online: true, Host: &self},
		{ID: "web1", Name: "web1"},
	}}
	s := New(Config{Collector: col, Host: host, SampleInterval: 250 * time.Millisecond})
	s.peers = fleet

	var servers []metrics.ServerSummary
	if err := json.Unmarshal(get(t, s, "/api/servers").Body.Bytes(), &servers); err != nil {
		t.Fatalf("unmarshal servers: %v", err)
	}
	var ids []string
	for _, sum := range servers {
		ids = append(ids, sum.ID)
	}
	if got, want := strings.Join(ids, ","), "malevik,web1"; got != want {
		t.Fatalf("server ids = %q, want %q (local hidden behind its alias)", got, want)
	}

	// A different machine (same hostname, different boot time) must NOT alias.
	other := host
	other.BootTime = host.BootTime - 12345
	fleet.setHost("malevik", other, 2000)
	if err := json.Unmarshal(get(t, s, "/api/servers").Body.Bytes(), &servers); err != nil {
		t.Fatalf("unmarshal servers: %v", err)
	}
	if len(servers) != 3 || servers[0].ID != "local" {
		t.Fatalf("expected local,malevik,web1 when boot times differ, got %d entries starting %q", len(servers), servers[0].ID)
	}
}
