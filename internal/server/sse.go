package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/CleveroAB/owlwatch/internal/metrics"
)

// heartbeatInterval is how often an SSE comment is written so intermediate
// proxies don't idle-close the connection. It doubles as the dead-peer
// detector: every write is armed with a 2×heartbeatInterval deadline, so a
// peer that vanished without a FIN — or that stays connected but stops
// reading — fails a write within that bound and the handler returns. It is
// also the overview stream's resync cadence (§9.4). A var only so tests can
// shorten the tick; treat it as a constant everywhere else.
var heartbeatInterval = 15 * time.Second

// helloPayload is the SSE hello event body (HelloEvent in the UI contract).
type helloPayload struct {
	Host       metrics.HostInfo   `json:"host"`
	Recent     []metrics.Snapshot `json:"recent"` // ring buffer, oldest first
	IntervalMs int64              `json:"intervalMs"`
}

// sseStream is the event-writing core shared by every SSE handler (local
// live, peer live, overview): standard stream headers, one flush per event
// and a fresh write deadline armed before every write.
//
// The deadline matters because ctx.Done() cannot interrupt a Write blocked on
// a peer that stopped reading (zero receive window): a stalled peer then
// errors the write instead of pinning the handler goroutine, its
// subscriptions and its tickers until shutdown. The middleware's
// statusRecorder implements Unwrap, so the response controller reaches the
// real connection.
type sseStream struct {
	w       http.ResponseWriter
	flusher http.Flusher
	rc      *http.ResponseController
}

// startSSE upgrades the response to an SSE stream. On a ResponseWriter that
// cannot stream it answers 500 and reports false.
func startSSE(w http.ResponseWriter) (*sseStream, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	return &sseStream{w: w, flusher: flusher, rc: http.NewResponseController(w)}, true
}

func (st *sseStream) armWriteDeadline() error {
	return st.rc.SetWriteDeadline(time.Now().Add(2 * heartbeatInterval))
}

// event marshals v and writes one SSE event. A marshal failure (only possible
// for non-finite floats in these payloads) is logged and skipped so one bad
// sample doesn't kill the stream; a nil error therefore means "keep serving".
// A write failure ends the stream via the returned error.
func (st *sseStream) event(name string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("sse: marshal %s: %v", name, err)
		return nil
	}
	if err := st.armWriteDeadline(); err != nil {
		return err
	}
	// data is a single line: json.Marshal never emits raw newlines.
	if _, err := fmt.Fprintf(st.w, "event: %s\ndata: %s\n\n", name, data); err != nil {
		return err
	}
	st.flusher.Flush()
	return nil
}

// heartbeat writes the SSE comment that keeps proxies from idle-closing the
// connection.
func (st *sseStream) heartbeat() error {
	if err := st.armWriteDeadline(); err != nil {
		return err
	}
	if _, err := io.WriteString(st.w, ": ping\n\n"); err != nil {
		return err
	}
	st.flusher.Flush()
	return nil
}

// handleLive streams collector snapshots as server-sent events: one hello
// event on connect, then a snapshot event per collector tick. It serves the
// legacy /api/live alias and, via handleServerLive, /api/servers/local/live.
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	// Subscribe before reading the ring buffer: a tick landing in between
	// shows up twice (harmless) rather than being lost.
	snaps, unsubscribe := s.cfg.Collector.Subscribe()
	defer unsubscribe()

	st, ok := startSSE(w)
	if !ok {
		return
	}

	recent := s.cfg.Collector.Recent()
	if recent == nil {
		recent = []metrics.Snapshot{}
	}
	if st.event("hello", helloPayload{
		Host:       s.cfg.Host,
		Recent:     recent,
		IntervalMs: s.cfg.SampleInterval.Milliseconds(),
	}) != nil {
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
		case snap, open := <-snaps:
			if !open {
				// The collector dropped this subscriber (too slow) or
				// stopped; end the response so the client reconnects.
				return
			}
			if st.event("snapshot", snap) != nil {
				return
			}
		case <-heartbeat.C:
			if st.heartbeat() != nil {
				return
			}
		}
	}
}
