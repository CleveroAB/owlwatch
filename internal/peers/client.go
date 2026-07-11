package peers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/CleveroAB/owlwatch/internal/metrics"
)

const (
	// ringCap matches the collector's default ring size (5 min at 2s), so a
	// peer dashboard opened through the hub gets the same instant backfill
	// as one opened directly on the peer.
	ringCap = 150

	// subscriberBuffer is the per-subscriber channel capacity — same
	// contract as internal/collector: a subscriber that falls further behind
	// loses events, the peer goroutines never block on it.
	subscriberBuffer = 16

	defaultBaseBackoff    = 2 * time.Second
	defaultMaxBackoff     = 30 * time.Second
	defaultStallTimeout   = 45 * time.Second
	defaultHistoryTimeout = 15 * time.Second

	// dialTimeout and responseHeaderTimeout bound the connection phases the
	// stall watchdog cannot see (it only counts response-body bytes): a peer
	// that accepts TCP but never answers must not pin its goroutine forever.
	dialTimeout           = 10 * time.Second
	responseHeaderTimeout = 15 * time.Second

	// maxHistoryBytes caps one history response body: a misbehaving peer
	// streaming a multi-GB JSON array must not cause huge hub allocations.
	maxHistoryBytes = 8 << 20

	// recentCPUPoints is the max length of ServerSummary.RecentCPU (§9.4):
	// enough for a 60-point overview sparkline.
	recentCPUPoints = 60
)

// Event is one live update from a peer, delivered on Subscribe channels.
type Event struct {
	ID       string            // server id
	Snapshot *metrics.Snapshot // non-nil for snapshot events
	Online   *bool             // non-nil for status transitions
	LastSeen int64             // unix ms
}

// peerState is everything the hub caches about one peer. All fields are
// guarded by Client.mu; peer itself is immutable.
type peerState struct {
	peer       Peer
	online     bool
	lastSeen   int64             // unix ms of the last snapshot received; 0 = never
	intervalMs int64             // from hello; 0 = unknown yet
	host       *metrics.HostInfo // from hello; nil until first seen
	latest     *metrics.Snapshot // most recent snapshot; nil until seen

	ring  []metrics.Snapshot // circular buffer of the most recent snapshots
	head  int                // next write position in ring
	count int                // number of valid entries in ring
}

// push appends one snapshot to the ring, evicting the oldest at capacity.
func (s *peerState) push(snap metrics.Snapshot) {
	s.ring[s.head] = snap
	s.head = (s.head + 1) % len(s.ring)
	if s.count < len(s.ring) {
		s.count++
	}
}

// seedRing replaces the ring content with recent (oldest first), keeping the
// newest ringCap entries when the peer sent more.
func (s *peerState) seedRing(recent []metrics.Snapshot) {
	s.head, s.count = 0, 0
	if n := len(recent); n > len(s.ring) {
		recent = recent[n-len(s.ring):]
	}
	for _, snap := range recent {
		s.push(snap)
	}
}

// recentCPU returns the ring's CPU usage percentages for the overview
// sparkline (§9.4): at most recentCPUPoints values, oldest first. When the
// ring holds more, it downsamples evenly across the ring, always keeping the
// newest sample. Nil when the ring is empty.
func (s *peerState) recentCPU() []float64 {
	if s.count == 0 {
		return nil
	}
	start := s.head - s.count
	if start < 0 {
		start += len(s.ring)
	}
	at := func(i int) float64 { // i-th oldest snapshot's CPU usage
		return s.ring[(start+i)%len(s.ring)].CPU.UsagePct
	}
	if s.count <= recentCPUPoints {
		out := make([]float64, s.count)
		for i := range out {
			out[i] = at(i)
		}
		return out
	}
	// Evenly spaced indices over [0, count-1]; k = recentCPUPoints-1 lands
	// exactly on count-1, so the newest sample is always kept.
	out := make([]float64, recentCPUPoints)
	for k := range out {
		out[k] = at(k * (s.count - 1) / (recentCPUPoints - 1))
	}
	return out
}

// snapshots returns a copy of the ring, oldest first.
func (s *peerState) snapshots() []metrics.Snapshot {
	out := make([]metrics.Snapshot, 0, s.count)
	start := s.head - s.count
	if start < 0 {
		start += len(s.ring)
	}
	for i := 0; i < s.count; i++ {
		out = append(out, s.ring[(start+i)%len(s.ring)])
	}
	return out
}

// Client aggregates live state from a fixed set of peers. Run starts one
// goroutine per peer that keeps an SSE connection to the peer's /api/live
// with exponential-backoff reconnects; accessors and Subscribe expose the
// cached fleet state.
type Client struct {
	peers  []Peer
	httpc  *http.Client
	errlog *rateLogger

	// Tunables, set to the documented defaults by NewClient; tests shrink
	// them (same trick as the collector's usageTimeout).
	baseBackoff    time.Duration // first reconnect delay (2s)
	maxBackoff     time.Duration // reconnect delay ceiling (30s)
	stallTimeout   time.Duration // no bytes on the stream for this long → reconnect (45s)
	historyTimeout time.Duration // bound on one History proxy call (15s)

	mu      sync.Mutex
	states  map[string]*peerState
	subs    map[uint64]chan Event
	nextSub uint64
	stopped bool // Run has returned; all subscriber channels are closed
}

// NewClient builds a Client for the given peers (typically from ParsePeers).
// Call Run to start connecting.
func NewClient(peers []Peer) *Client {
	c := &Client{
		peers:  append([]Peer(nil), peers...),
		errlog: newRateLogger(time.Minute),
		httpc: &http.Client{
			// No overall Timeout: /api/live streams indefinitely. History
			// calls are bounded per-request by historyTimeout instead.
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   dialTimeout,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: responseHeaderTimeout,
			},
			// Peer URLs are exact bases (§9.1): a redirect means
			// misconfiguration and must fail, not be followed — it could
			// leak the bearer token to wherever Location points.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// %q: the Location value is peer-controlled — never log it raw.
				return fmt.Errorf("peers: refusing redirect to %q", req.URL)
			},
		},
		baseBackoff:    defaultBaseBackoff,
		maxBackoff:     defaultMaxBackoff,
		stallTimeout:   defaultStallTimeout,
		historyTimeout: defaultHistoryTimeout,
		states:         make(map[string]*peerState, len(peers)),
		subs:           make(map[uint64]chan Event),
	}
	for _, p := range c.peers {
		c.states[p.ID] = &peerState{peer: p, ring: make([]metrics.Snapshot, ringCap)}
	}
	return c
}

// Run blocks until ctx is cancelled, maintaining one connection goroutine per
// peer. When it returns, every subscriber channel has been closed.
func (c *Client) Run(ctx context.Context) {
	defer c.shutdown()

	var wg sync.WaitGroup
	for _, p := range c.peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.runPeer(ctx, p)
		}()
	}
	wg.Wait()
	// The peer goroutines only return once ctx is done; with zero peers Run
	// still blocks until cancelled so callers can treat it uniformly.
	<-ctx.Done()
}

// runPeer is one peer's connect loop: stream until the connection breaks or
// stalls, mark the peer offline, back off (2s→30s, jittered, reset after any
// successful connect) and try again.
func (c *Client) runPeer(ctx context.Context, p Peer) {
	backoff := c.baseBackoff
	for {
		connected := c.stream(ctx, p)
		c.setOnline(p, false)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = c.baseBackoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter(backoff)):
		}
		backoff *= 2
		if backoff > c.maxBackoff {
			backoff = c.maxBackoff
		}
	}
}

// jitter spreads reconnect attempts uniformly over [d/2, d) so a fleet of
// peers lost at once doesn't reconnect in lockstep.
func jitter(d time.Duration) time.Duration {
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + time.Duration(rand.Int64N(int64(half)))
}

// Subscribe returns a channel of muxed events for all peers plus a cancel
// func. The channel is buffered; a subscriber that falls behind loses events
// rather than blocking a peer goroutine. The channel is closed by the
// (idempotent) cancel func, or when Run exits.
func (c *Client) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subscriberBuffer)

	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	id := c.nextSub
	c.nextSub++
	c.subs[id] = ch
	c.mu.Unlock()

	cancel := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if sub, ok := c.subs[id]; ok {
			delete(c.subs, id)
			close(sub)
		}
	}
	return ch, cancel
}

// shutdown marks the client stopped and closes all subscriber channels.
func (c *Client) shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	for id, ch := range c.subs {
		delete(c.subs, id)
		close(ch)
	}
}

// broadcastLocked fans an event out to subscribers. Sends are non-blocking: a
// subscriber with a full buffer loses this event instead of stalling a peer
// goroutine. Callers must hold c.mu.
func (c *Client) broadcastLocked(ev Event) {
	for _, ch := range c.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// setOnline records a peer's connection state and broadcasts a status event
// on transitions (repeated failures while already offline stay silent).
func (c *Client) setOnline(p Peer, online bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.states[p.ID]
	if st.online == online {
		return
	}
	st.online = online
	o := online
	c.broadcastLocked(Event{ID: p.ID, Online: &o, LastSeen: st.lastSeen})
}

// applyHello caches a peer's hello payload: host info, sample interval and
// the ring seed. Per the contract, hello itself is not broadcast — the online
// transition was already announced when the connection succeeded.
func (c *Client) applyHello(p Peer, hello helloPayload) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.states[p.ID]
	host := hello.Host
	st.host = &host
	st.intervalMs = hello.IntervalMs
	st.seedRing(hello.Recent)
	if n := len(hello.Recent); n > 0 {
		last := hello.Recent[n-1]
		st.latest = &last
		st.lastSeen = time.Now().UnixMilli()
	}
}

// applySnapshot records one live snapshot and broadcasts it.
func (c *Client) applySnapshot(p Peer, snap metrics.Snapshot) {
	now := time.Now().UnixMilli()
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.states[p.ID]
	st.latest = &snap
	st.push(snap)
	st.lastSeen = now
	c.broadcastLocked(Event{ID: p.ID, Snapshot: &snap, LastSeen: now})
}

// Servers returns the current fleet state, peers only, in configured order.
// Host/Latest are copies: nil until the peer's hello / first snapshot.
func (c *Client) Servers() []metrics.ServerSummary {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]metrics.ServerSummary, 0, len(c.peers))
	for _, p := range c.peers {
		st := c.states[p.ID]
		sum := metrics.ServerSummary{
			ID:         p.ID,
			Name:       p.Name,
			Online:     st.online,
			LastSeen:   st.lastSeen,
			IntervalMs: st.intervalMs,
			RecentCPU:  st.recentCPU(),
		}
		if st.host != nil {
			h := *st.host
			sum.Host = &h
		}
		if st.latest != nil {
			l := *st.latest
			sum.Latest = &l
		}
		out = append(out, sum)
	}
	return out
}

// HostInfo returns the cached host info for a peer; ok is false for an
// unknown id. A known peer whose hello has not been seen yet reports the zero
// HostInfo (its Servers() entry keeps Host nil — callers that must
// distinguish "configured but never reached" use that).
func (c *Client) HostInfo(id string) (metrics.HostInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.states[id]
	if !ok {
		return metrics.HostInfo{}, false
	}
	if st.host == nil {
		return metrics.HostInfo{}, true
	}
	return *st.host, true
}

// Recent returns a copy of a peer's snapshot ring, oldest first (cap 150).
// It is nil for an unknown id and empty (non-nil) for a peer with no data.
func (c *Client) Recent(id string) []metrics.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.states[id]
	if !ok {
		return nil
	}
	return st.snapshots()
}

// IntervalMs returns a peer's sample interval from its hello; 0 when the id
// is unknown or the peer has not been seen yet.
func (c *Client) IntervalMs(id string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.states[id]
	if !ok {
		return 0
	}
	return st.intervalMs
}

// History proxies GET <peer>/api/history?range=<rangeKey> with a 15s timeout.
// It returns ErrUnknownPeer for an id that is not configured and
// ErrPeerUnavailable (wrapped) when the peer cannot be reached, times out, or
// answers with anything but valid 200 JSON.
func (c *Client) History(ctx context.Context, id, rangeKey string) ([]metrics.HistoryPoint, error) {
	c.mu.Lock()
	st, ok := c.states[id]
	c.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("peers: %q: %w", id, ErrUnknownPeer)
	}
	p := st.peer

	ctx, cancel := context.WithTimeout(ctx, c.historyTimeout)
	defer cancel()

	reqURL := p.URL.String() + "/api/history?range=" + url.QueryEscape(rangeKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("peers: %s: building history request: %w", id, err)
	}
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("peers: %s: history: %v: %w", id, err, ErrPeerUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("peers: %s: history returned %q: %w", id, resp.Status, ErrPeerUnavailable)
	}
	var body struct {
		Points []metrics.HistoryPoint `json:"points"`
	}
	// LimitReader bounds the allocation a misbehaving peer can force; a body
	// truncated at the cap fails the decode → ErrPeerUnavailable below.
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxHistoryBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("peers: %s: decoding history: %v: %w", id, err, ErrPeerUnavailable)
	}
	if body.Points == nil {
		body.Points = []metrics.HistoryPoint{}
	}
	return body.Points, nil
}

// rateLogger logs a given message key at most once per interval, so a peer
// that fails on every reconnect attempt doesn't flood the log — same pattern
// as internal/collector.
type rateLogger struct {
	every time.Duration

	mu   sync.Mutex
	last map[string]time.Time
}

func newRateLogger(every time.Duration) *rateLogger {
	return &rateLogger{every: every, last: make(map[string]time.Time)}
}

func (l *rateLogger) printf(key, format string, args ...any) {
	l.mu.Lock()
	now := time.Now()
	if t, ok := l.last[key]; ok && now.Sub(t) < l.every {
		l.mu.Unlock()
		return
	}
	l.last[key] = now
	l.mu.Unlock()
	log.Printf(format, args...)
}
