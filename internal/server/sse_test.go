package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CleveroAB/owlwatch/internal/collector"
)

// The hello event must carry the sample interval so the UI can scale its
// live window (DESIGN.md §3.3). Streaming it through a real server also
// proves the per-write deadline reaches the connection via the middleware
// chain — a failing SetWriteDeadline would abort the stream before hello.
func TestHandleLiveHelloCarriesIntervalMs(t *testing.T) {
	col := collector.New(collector.Config{SampleInterval: 250 * time.Millisecond})
	s := New(Config{
		Collector:      col,
		Host:           col.HostInfo(),
		SampleInterval: 250 * time.Millisecond,
	})

	srv := httptest.NewServer(s.handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/live", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/live: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// Read the first SSE event: an "event:" line, a "data:" line, blank line.
	var event, data string
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			event = v
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok {
			data = v
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if event != "hello" {
		t.Fatalf("first event = %q, want %q", event, "hello")
	}

	var hello struct {
		IntervalMs int64 `json:"intervalMs"`
	}
	if err := json.Unmarshal([]byte(data), &hello); err != nil {
		t.Fatalf("unmarshal hello data %q: %v", data, err)
	}
	if hello.IntervalMs != 250 {
		t.Fatalf("hello intervalMs = %d, want 250", hello.IntervalMs)
	}
}

// Before the first sample, healthz must be 503 so Docker does not report a
// container healthy while the sampler has produced nothing.
func TestHealthzNoSampleIs503(t *testing.T) {
	col := collector.New(collector.Config{SampleInterval: time.Second}) // Run never called: no samples
	s := New(Config{Collector: col, SampleInterval: time.Second})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "127.0.0.1:8080" // pass the host check, as -healthcheck does
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}
