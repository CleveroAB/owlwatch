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
}

// Server serves the JSON API, the SSE live stream and the embedded UI.
type Server struct {
	cfg     Config
	handler http.Handler
}

// New builds the route table and middleware chain.
func New(cfg Config) *Server {
	// The UI contract types gpuNames as an array; never let it marshal as null.
	if cfg.Host.GPUNames == nil {
		cfg.Host.GPUNames = []string{}
	}
	s := &Server{cfg: cfg}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/host", s.handleHost)
	mux.HandleFunc("GET /api/live", s.handleLive)
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.Handle("/", newUIHandler())

	s.handler = withLogging(withRecovery(withHostCheck(cfg.AllowedHosts, mux)))
	return s
}

// ListenAndServe serves until ctx is cancelled, then shuts down gracefully.
//
// ReadTimeout and WriteTimeout are deliberately zero: /api/live holds a
// response open indefinitely. A non-zero WriteTimeout would cut every SSE
// stream after a fixed interval, and a non-zero ReadTimeout arms a
// whole-connection read deadline that net/http's background read trips
// mid-stream, cancelling the request context. Slow-loris protection comes
// from ReadHeaderTimeout instead, and dead or stalled SSE peers are reaped by
// the per-write deadlines handleLive arms through http.ResponseController.
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
