// Package server exposes the owlwatch HTTP API (JSON + SSE) and serves the
// embedded web UI. Routing is stdlib http.ServeMux with Go 1.22 method
// patterns; no router dependency.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/CleveroAB/owlwatch/internal/collector"
	"github.com/CleveroAB/owlwatch/internal/metrics"
	"github.com/CleveroAB/owlwatch/internal/peers"
	"github.com/CleveroAB/owlwatch/internal/store"
)

// Config wires the server to its collaborators.
type Config struct {
	Addr           string // ":8080"
	Collector      *collector.Collector
	Store          *store.Store
	Host           metrics.HostInfo // Version already filled in
	SampleInterval time.Duration    // collector cadence: hello intervalMs and the healthz staleness bound
	AllowedHosts   []string         // OWLWATCH_ALLOWED_HOSTS: extra Host header names accepted by withHostCheck
	Peers          *peers.Client    // OWLWATCH_PEERS hub state (DESIGN.md §9); nil = standalone
	Token          string           // OWLWATCH_TOKEN: required on /api/ routes when non-empty
}

// Server serves the JSON API, the SSE live streams and the embedded UI.
type Server struct {
	cfg     Config
	peers   peerSource // cfg.Peers behind the internal test seam; nil when standalone
	handler http.Handler
}

// New builds the route table and middleware chain.
func New(cfg Config) *Server {
	// The UI contract types gpuNames as an array; never let it marshal as null.
	if cfg.Host.GPUNames == nil {
		cfg.Host.GPUNames = []string{}
	}
	s := &Server{cfg: cfg}
	if cfg.Peers != nil {
		// Only assign a non-nil client: a nil *peers.Client wrapped in a
		// non-nil interface would defeat the s.peers == nil standalone checks.
		s.peers = cfg.Peers
	}

	mux := http.NewServeMux()
	// Fleet API (DESIGN.md §9.4), registered even when standalone — the UI
	// consumes only this surface.
	mux.HandleFunc("GET /api/servers", s.handleServers)
	mux.HandleFunc("GET /api/servers/{id}/host", s.handleServerHost)
	mux.HandleFunc("GET /api/servers/{id}/history", s.handleServerHistory)
	mux.HandleFunc("GET /api/servers/{id}/live", s.handleServerLive)
	mux.HandleFunc("GET /api/overview/live", s.handleOverviewLive)
	// Legacy aliases for the local server. This surface is frozen: it is
	// what a hub consumes on its peers (DESIGN.md §4).
	mux.HandleFunc("GET /api/host", s.handleHost)
	mux.HandleFunc("GET /api/live", s.handleLive)
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.Handle("/", newUIHandler())

	// Token auth sits inside the host check so a rebound Host is answered
	// with 421 before it can even probe whether auth is on.
	s.handler = withLogging(withRecovery(withHostCheck(cfg.AllowedHosts,
		withTokenAuth(cfg.Token, mux))))
	return s
}

// ListenAndServe serves until ctx is cancelled, then shuts down gracefully.
//
// ReadTimeout and WriteTimeout are deliberately zero: the SSE routes
// (/api/live, /api/servers/{id}/live, /api/overview/live) hold a response
// open indefinitely. A non-zero WriteTimeout would cut every SSE stream
// after a fixed interval, and a non-zero ReadTimeout arms a whole-connection
// read deadline that net/http's background read trips mid-stream, cancelling
// the request context. Slow-loris protection comes from ReadHeaderTimeout
// instead, and dead or stalled SSE peers are reaped by the per-write
// deadlines sseStream arms through http.ResponseController.
// BaseContext derives every request context from ctx,
// so cancelling ctx ends all in-flight SSE streams and lets Shutdown drain
// promptly.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       0,
		WriteTimeout:      0,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		return err // bind or accept failure; ctx is still live
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := srv.Shutdown(shutdownCtx)
	if err != nil {
		srv.Close() // grace period exceeded: drop remaining connections
	}
	if serveErr := <-errCh; serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && err == nil {
		err = serveErr
	}
	return err
}

func (s *Server) handleHost(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Host)
}

type historyResponse struct {
	Range  string                 `json:"range"`
	Points []metrics.HistoryPoint `json:"points"`
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("range")
	rng, ok := store.Ranges[key]
	if !ok {
		writeJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("unknown range %q (want 1h, 6h, 24h, 7d or 30d)", key))
		return
	}
	// r.Context() so an aborted request (e.g. a rapid range switch in the UI)
	// cancels the query instead of occupying the store's single SQLite
	// connection to completion.
	points, err := s.cfg.Store.QueryContext(r.Context(), rng, time.Now())
	if err != nil {
		log.Printf("history query range=%s: %v", key, err)
		writeJSONError(w, http.StatusInternalServerError, "history query failed")
		return
	}
	if points == nil {
		points = []metrics.HistoryPoint{} // contract: empty, never null
	}
	writeJSON(w, http.StatusOK, historyResponse{Range: rng.Key, Points: points})
}

// handleHealthz reports 200 only while the sampler is live: a sample must
// exist and be no older than 5× the sample interval (DESIGN.md §3.3). A
// wedged sampler therefore flips health, and Docker's HEALTHCHECK restarts
// the container.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	snap, ok := s.cfg.Collector.Latest()
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, "no sample yet")
		return
	}
	if age := time.Since(time.UnixMilli(snap.TS)); age > 5*s.cfg.SampleInterval {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, "last sample is %s old; sampler looks wedged\n", age.Round(time.Second))
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ok")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json response: %v", err)
	}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
