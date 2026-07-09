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
// reading — fails a write within that bound and the handler returns.
const heartbeatInterval = 15 * time.Second

// helloPayload is the SSE hello event body (HelloEvent in the UI contract).
type helloPayload struct {
	Host       metrics.HostInfo   `json:"host"`
	Recent     []metrics.Snapshot `json:"recent"` // ring buffer, oldest first
	IntervalMs int64              `json:"intervalMs"`
}

// handleLive streams collector snapshots as server-sent events: one hello
// event on connect, then a snapshot event per collector tick.
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Subscribe before reading the ring buffer: a tick landing in between
	// shows up twice (harmless) rather than being lost.
	snaps, unsubscribe := s.cfg.Collector.Subscribe()
	defer unsubscribe()

	// ctx.Done() cannot interrupt a Write blocked on a peer that stopped
	// reading (zero receive window), so arm a fresh deadline before every
	// write: a stalled peer then errors the write instead of pinning this
	// goroutine, its subscription and the ticker until shutdown. The
	// middleware's statusRecorder implements Unwrap, so the controller
	// reaches the real connection.
	rc := http.NewResponseController(w)
	armWriteDeadline := func() error {
		return rc.SetWriteDeadline(time.Now().Add(2 * heartbeatInterval))
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	recent := s.cfg.Collector.Recent()
	if recent == nil {
		recent = []metrics.Snapshot{}
	}
	hello, err := json.Marshal(helloPayload{
		Host:       s.cfg.Host,
		Recent:     recent,
		IntervalMs: s.cfg.SampleInterval.Milliseconds(),
	})
	if err != nil {
		log.Printf("sse: marshal hello: %v", err)
		return
	}
	if armWriteDeadline() != nil {
		return
	}
	if err := writeSSE(w, "hello", hello); err != nil {
		return
	}
	flusher.Flush()

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
			data, err := json.Marshal(snap)
			if err != nil {
				log.Printf("sse: marshal snapshot: %v", err)
				continue
			}
			if armWriteDeadline() != nil {
				return
			}
			if err := writeSSE(w, "snapshot", data); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if armWriteDeadline() != nil {
				return
			}
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSE writes one event in SSE wire format. data must be a single line,
// which json.Marshal guarantees (it never emits raw newlines).
func writeSSE(w io.Writer, event string, data []byte) error {
	_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	return err
}
