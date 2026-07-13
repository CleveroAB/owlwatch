package peers

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/CleveroAB/owlwatch/internal/metrics"
)

// maxEventBytes caps one SSE line and one accumulated event payload. A normal
// hello with 150 compact snapshots is far smaller; keeping this at 128 KiB
// prevents a malformed peer from turning the 150-item cache into a large
// byte-retention attack.
const maxEventBytes = 128 << 10

// helloPayload mirrors the /api/live hello event body served by
// internal/server/sse.go (the wire contract in DESIGN.md §4).
type helloPayload struct {
	Host       metrics.HostInfo   `json:"host"`
	Recent     []metrics.Snapshot `json:"recent"` // ring buffer, oldest first
	IntervalMs int64              `json:"intervalMs"`
}

// stream opens one SSE connection to a peer's /api/live and consumes it until
// the connection breaks, stalls, or ctx is cancelled. It reports whether a
// 200 response was received, which resets the caller's backoff.
func (c *Client) stream(ctx context.Context, p Peer) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL.String()+"/api/live", nil)
	if err != nil {
		c.errlog.printf(p.ID, "peers: %s: building live request: %v", p.ID, err)
		return false
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		c.errlog.printf(p.ID, "peers: %s: connecting to %s: %v", p.ID, p.URL, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.errlog.printf(p.ID, "peers: %s: /api/live returned %q", p.ID, resp.Status)
		return false
	}
	// A 200 from anything that is not an SSE endpoint (captive portal, wrong
	// URL pointing at some web service) must not count as a healthy connect —
	// it would broadcast Online:true and reset the backoff, flapping forever.
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || mt != "text/event-stream" {
		c.errlog.printf(p.ID, "peers: %s: /api/live returned Content-Type %q, want text/event-stream", p.ID, resp.Header.Get("Content-Type"))
		return false
	}
	// Stall watchdog: any received bytes (heartbeat comments included) reset
	// the timer; total silence for stallTimeout closes the body, which
	// unblocks the pending read and ends this connection.
	body := &activityReader{r: resp.Body, activity: make(chan struct{}, 1)}
	stop := make(chan struct{})
	defer close(stop)
	go c.watchStall(resp.Body, body.activity, stop)

	c.readEvents(p, body)
	return true
}

// watchStall closes body when no read activity is signalled for stallTimeout.
// It returns when stop is closed (the stream ended for another reason) or
// after firing.
func (c *Client) watchStall(body io.Closer, activity <-chan struct{}, stop <-chan struct{}) {
	timer := time.NewTimer(c.stallTimeout)
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-activity:
			// Sole receiver of timer.C: when Stop reports the timer already
			// fired, the value is still buffered — drain before Reset.
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(c.stallTimeout)
		case <-timer.C:
			body.Close()
			return
		}
	}
}

// activityReader signals (without blocking) on every successful read, feeding
// the stall watchdog.
type activityReader struct {
	r        io.Reader
	activity chan struct{}
}

func (a *activityReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 {
		select {
		case a.activity <- struct{}{}:
		default:
		}
	}
	return n, err
}

// readEvents is a minimal SSE parser: it tracks "event:" and "data:" lines,
// dispatches on blank lines and ignores comments and unknown fields. It
// returns when the stream ends (connection closed, stalled, or ctx aborted
// the request).
func (c *Client) readEvents(p Peer, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxEventBytes)
	var event string
	var data []byte
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		switch {
		case line == "":
			if len(data) > 0 {
				c.dispatch(p, event, data)
			}
			event, data = "", nil
		case strings.HasPrefix(line, ":"):
			// Comment (heartbeat) — its bytes already reset the stall timer.
		case strings.HasPrefix(line, "event:"):
			event = trimFieldValue(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			value := trimFieldValue(line[len("data:"):])
			// The scanner caps single lines, but a peer streaming endless
			// data lines (never a blank line) would otherwise accumulate an
			// unbounded payload: enforce the cap on the accumulated event
			// too, and abort the connection — reconnect/backoff takes over.
			if len(data)+len(value)+1 > maxEventBytes {
				c.errlog.printf(p.ID, "peers: %s: SSE event exceeds %d bytes, dropping connection", p.ID, maxEventBytes)
				return
			}
			if len(data) > 0 {
				data = append(data, '\n') // multi-line data joins with \n per the SSE spec
			}
			data = append(data, value...)
		default:
			// Other fields (id:, retry:) are irrelevant here.
		}
	}
	if err := scanner.Err(); err != nil {
		c.errlog.printf(p.ID, "peers: %s: live stream ended: %v", p.ID, err)
	}
}

// trimFieldValue strips the single optional space after an SSE field colon.
func trimFieldValue(v string) string {
	return strings.TrimPrefix(v, " ")
}

// dispatch decodes and applies one SSE event. Undecodable payloads are logged
// (rate-limited) and skipped — one bad event must not kill the connection.
func (c *Client) dispatch(p Peer, event string, data []byte) {
	switch event {
	case "hello":
		var hello helloPayload
		if err := json.Unmarshal(data, &hello); err != nil {
			c.errlog.printf(p.ID+":hello", "peers: %s: decoding hello: %v", p.ID, err)
			return
		}
		c.applyHello(p, hello)
		// A transport-level 200 is not enough to call a peer healthy. Only a
		// decodable hello proves this is a compatible owlwatch endpoint.
		c.setOnline(p, true)
	case "snapshot":
		var snap metrics.Snapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			c.errlog.printf(p.ID+":snapshot", "peers: %s: decoding snapshot: %v", p.ID, err)
			return
		}
		c.applySnapshot(p, snap)
	}
}
