package server

// This file is the federation surface (DESIGN.md §9.4): the /api/servers
// fleet API and the overview SSE mux. These routes are registered even when
// standalone — the UI consumes only this surface; the unprefixed
// /api/host|live|history aliases exist for hubs consuming this instance as a
// peer.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/CleveroAB/owlwatch/internal/metrics"
	"github.com/CleveroAB/owlwatch/internal/peers"
	"github.com/CleveroAB/owlwatch/internal/store"
)

// peerSource is the subset of *peers.Client the handlers consume. The public
// Config field stays the concrete *peers.Client (DESIGN.md §9.4 contract);
// this internal seam exists so handler tests can substitute a fake fleet
// without running peer connections.
type peerSource interface {
	Servers() []metrics.ServerSummary
	Subscribe() (<-chan peers.Event, func())
	Recent(id string) []metrics.Snapshot
	History(ctx context.Context, id, rangeKey string) ([]metrics.HistoryPoint, error)
}

// overviewSnapshotPayload is the overview `snapshot` event body
// (OverviewSnapshotEvent in the UI contract).
type overviewSnapshotPayload struct {
	ID       string           `json:"id"`
	Snapshot metrics.Snapshot `json:"snapshot"`
}

// overviewStatusPayload is the `status` event body (OverviewStatusEvent in
// the UI contract), used on the overview stream for peer transitions and on
// a peer's live stream for the §9.6 not-yet-seen case.
type overviewStatusPayload struct {
	ID       string `json:"id"`
	Online   bool   `json:"online"`
	LastSeen int64  `json:"lastSeen"` // unix ms; 0 = never seen
}

// localSummary describes this instance for /api/servers and the overview
// stream. The local server is always online; Latest/LastSeen stay empty until
// the collector's first sample. RecentCPU comes from the collector ring so
// overview sparklines render on first paint (§9.4); peer entries carry it
// from Peers.Servers().
func (s *Server) localSummary() metrics.ServerSummary {
	sum := metrics.ServerSummary{
		ID:         "local",
		Name:       s.cfg.Host.Hostname,
		Local:      true,
		Online:     true,
		IntervalMs: s.cfg.SampleInterval.Milliseconds(),
		Host:       &s.cfg.Host,
		RecentCPU:  recentCPU(s.cfg.Collector.Recent()),
	}
	if snap, ok := s.cfg.Collector.Latest(); ok {
		sum.Latest = &snap
		sum.LastSeen = snap.TS
	}
	return sum
}

// recentCPUPoints caps ServerSummary.RecentCPU: enough for a 60-point
// overview sparkline (§9.4).
const recentCPUPoints = 60

// recentCPU extracts the sparkline series from a snapshot ring: CPU usagePct,
// oldest first, downsampled evenly to ≤recentCPUPoints values (keeping the
// oldest and the newest sample) when the ring holds more.
func recentCPU(snaps []metrics.Snapshot) []float64 {
	if len(snaps) == 0 {
		return nil
	}
	if len(snaps) <= recentCPUPoints {
		out := make([]float64, len(snaps))
		for i, snap := range snaps {
			out[i] = snap.CPU.UsagePct
		}
		return out
	}
	out := make([]float64, recentCPUPoints)
	for i := range out {
		idx := i * (len(snaps) - 1) / (recentCPUPoints - 1)
		out[i] = snaps[idx].CPU.UsagePct
	}
	return out
}

// localAliasedBy reports the configured peer that is this same machine, or ""
// when none is. A hub listed in its own OWLWATCH_PEERS (say, via its public
// domain) would otherwise show up twice; same hostname + same boot time
// identifies "same machine" without any extra wiring. The operator-named
// peer entry wins over the implicit "local" one.
func (s *Server) localAliasedBy() string {
	if s.peers == nil {
		return ""
	}
	for _, sum := range s.peers.Servers() {
		if sum.Host != nil && sum.Host.BootTime != 0 &&
			sum.Host.Hostname == s.cfg.Host.Hostname && sum.Host.BootTime == s.cfg.Host.BootTime {
			return sum.ID
		}
	}
	return ""
}

// allServers returns the fleet: local first, then peers in configured order.
// When a peer aliases the local machine (localAliasedBy), the local entry is
// omitted so the fleet shows one card per machine, under the operator's name.
func (s *Server) allServers() []metrics.ServerSummary {
	var list []metrics.ServerSummary
	if s.localAliasedBy() == "" {
		list = append(list, s.localSummary())
	}
	if s.peers != nil {
		list = append(list, s.peers.Servers()...)
	}
	return list
}

// peerSummary looks up a configured peer by id. It reports false for
// "local", for unknown ids and when the instance is standalone.
func (s *Server) peerSummary(id string) (metrics.ServerSummary, bool) {
	if s.peers == nil {
		return metrics.ServerSummary{}, false
	}
	for _, sum := range s.peers.Servers() {
		if sum.ID == id {
			return sum, true
		}
	}
	return metrics.ServerSummary{}, false
}

func (s *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.allServers())
}

func (s *Server) handleServerHost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "local" {
		s.handleHost(w, r)
		return
	}
	sum, ok := s.peerSummary(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown server")
		return
	}
	if sum.Host == nil {
		// Configured but never reached: no HostInfo cached yet (§9.6).
		writeJSONError(w, http.StatusBadGateway, "peer unreachable")
		return
	}
	writeJSON(w, http.StatusOK, sum.Host)
}

func (s *Server) handleServerHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "local" {
		s.handleHistory(w, r)
		return
	}
	if _, ok := s.peerSummary(id); !ok {
		writeJSONError(w, http.StatusNotFound, "unknown server")
		return
	}
	// Validate the range on the hub so a typo answers 400 here instead of
	// surfacing as a confusing peer error.
	key := r.URL.Query().Get("range")
	rng, ok := store.Ranges[key]
	if !ok {
		writeJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("unknown range %q (want 1h, 6h, 24h, 7d or 30d)", key))
		return
	}
	points, err := s.peers.History(r.Context(), id, rng.Key)
	switch {
	case errors.Is(err, peers.ErrUnknownPeer):
		writeJSONError(w, http.StatusNotFound, "unknown server")
		return
	case errors.Is(err, peers.ErrPeerUnavailable):
		writeJSONError(w, http.StatusBadGateway, "peer unreachable")
		return
	case err != nil:
		log.Printf("proxy history id=%s range=%s: %v", id, key, err)
		writeJSONError(w, http.StatusBadGateway, "peer unreachable")
		return
	}
	if points == nil {
		points = []metrics.HistoryPoint{} // contract: empty, never null
	}
	writeJSON(w, http.StatusOK, historyResponse{Range: rng.Key, Points: points})
}

// handleServerLive streams one server's live data in exactly the v1 wire
// format: one hello on connect, then snapshot events, with heartbeats. For
// "local" it is the existing collector stream; for a peer it is served from
// the hub's cached state plus the shared peers subscription filtered to that
// id — the hub never dials a second upstream connection per viewer (§9.4).
// Peer streams additionally carry upstream reachability as status events:
// every online/offline transition is forwarded, and a viewer connecting
// while the peer is offline is told so immediately — a dashboard must never
// show a dead peer as live (§9.4).
func (s *Server) handleServerLive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "local" {
		s.handleLive(w, r)
		return
	}
	if _, ok := s.peerSummary(id); !ok {
		writeJSONError(w, http.StatusNotFound, "unknown server")
		return
	}

	// Subscribe before reading the cached state: a hello or snapshot landing
	// in between shows up twice (harmless) rather than being lost.
	events, unsubscribe := s.peers.Subscribe()
	defer unsubscribe()

	st, ok := startSSE(w)
	if !ok {
		return
	}

	// A cached HostInfo means the peer has been reached at least once: send
	// the v1 hello straight from the cache (with recent: [] if the ring is
	// empty), then — if the peer is currently offline — a status event so the
	// viewer never renders the cached data as live (§9.4). Without a cached
	// HostInfo §9.6 applies: report the peer offline via a status event and
	// send hello once its first hello has been cached.
	sum, _ := s.peerSummary(id)
	helloSent := sum.Host != nil
	if helloSent {
		if st.event("hello", s.peerHello(sum)) != nil {
			return
		}
		if !sum.Online {
			if st.event("status", overviewStatusPayload{ID: id, Online: false, LastSeen: sum.LastSeen}) != nil {
				return
			}
		}
	} else if st.event("status", overviewStatusPayload{ID: id, Online: false, LastSeen: sum.LastSeen}) != nil {
		return
	}

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			// Client disconnected or the server is shutting down.
			return
		case ev, open := <-events:
			if !open {
				// The peers client dropped this subscriber (too slow) or
				// stopped; end the response so the client reconnects.
				return
			}
			if ev.ID != id {
				continue
			}
			helloDeferred := false
			if !helloSent {
				// Any event for this peer may mean its hello is now cached
				// (the client caches HostInfo and seeds the ring before
				// broadcasting), so re-check and send the deferred hello.
				if sum, ok := s.peerSummary(id); ok && sum.Host != nil {
					if st.event("hello", s.peerHello(sum)) != nil {
						return
					}
					helloSent = true
					helloDeferred = true
				}
			}
			switch {
			case ev.Online != nil:
				// Forward every upstream online/offline transition (§9.4).
				if st.event("status", overviewStatusPayload{ID: id, Online: *ev.Online, LastSeen: ev.LastSeen}) != nil {
					return
				}
			case ev.Snapshot != nil:
				if !helloSent || helloDeferred {
					// This snapshot is already in the ring that the deferred
					// hello carried (or will carry); don't send it twice.
					continue
				}
				if st.event("snapshot", *ev.Snapshot) != nil {
					return
				}
			}
		case <-heartbeat.C:
			if st.heartbeat() != nil {
				return
			}
		}
	}
}

// peerHello builds the v1 hello payload for a peer from the hub's cache.
// sum.Host must be non-nil.
func (s *Server) peerHello(sum metrics.ServerSummary) helloPayload {
	recent := s.peers.Recent(sum.ID)
	if recent == nil {
		recent = []metrics.Snapshot{}
	}
	return helloPayload{Host: *sum.Host, Recent: recent, IntervalMs: sum.IntervalMs}
}

// handleOverviewLive streams the whole fleet on one connection (§9.4): a
// servers event with the full state on connect, then muxed snapshot events
// (local collector ticks + peer snapshots) and status events on peer
// transitions. The full servers event is re-sent on every peer transition
// and on each heartbeat tick (level-triggered resync: a late peer hello, or
// a status event dropped by the non-blocking broadcast, heals within one
// cycle; the client merges by replacing). Standalone it degrades to servers
// + local snapshots.
func (s *Server) handleOverviewLive(w http.ResponseWriter, r *http.Request) {
	// Subscribe to both sources before snapshotting the fleet state, for the
	// same duplicate-over-loss reason as everywhere else.
	snaps, unsubscribe := s.cfg.Collector.Subscribe()
	defer unsubscribe()

	// A nil channel never fires in the select below, so standalone mode
	// needs no special casing past this point.
	var events <-chan peers.Event
	if s.peers != nil {
		ch, unsubscribePeers := s.peers.Subscribe()
		defer unsubscribePeers()
		events = ch
	}

	st, ok := startSSE(w)
	if !ok {
		return
	}
	if st.event("servers", s.allServers()) != nil {
		return
	}

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case snap, open := <-snaps:
			if !open {
				return
			}
			// When a configured peer aliases this machine, its stream carries
			// the data — suppress the duplicate local events (the peer's own
			// snapshots arrive via the peers channel under the peer's id).
			if s.localAliasedBy() != "" {
				continue
			}
			if st.event("snapshot", overviewSnapshotPayload{ID: "local", Snapshot: snap}) != nil {
				return
			}
		case ev, open := <-events:
			if !open {
				return
			}
			switch {
			case ev.Snapshot != nil:
				if st.event("snapshot", overviewSnapshotPayload{ID: ev.ID, Snapshot: *ev.Snapshot}) != nil {
					return
				}
			case ev.Online != nil:
				if st.event("status", overviewStatusPayload{ID: ev.ID, Online: *ev.Online, LastSeen: ev.LastSeen}) != nil {
					return
				}
				// Transition resync: re-send the full state, built from the
				// same cheap cached assembly as /api/servers (§9.4).
				if st.event("servers", s.allServers()) != nil {
					return
				}
			}
		case <-heartbeat.C:
			if st.heartbeat() != nil {
				return
			}
			// Heartbeat-tick resync (§9.4), same assembly as above.
			if st.event("servers", s.allServers()) != nil {
				return
			}
		}
	}
}
