package metrics

// ServerSummary describes one monitored server as seen by an owlwatch
// instance. A standalone instance reports exactly one (the local server);
// a hub (OWLWATCH_PEERS set) reports the local server plus every peer.
//
// THIS TYPE IS PART OF THE CONTRACT — keep in sync with web/src/lib/types.ts.
type ServerSummary struct {
	ID         string    `json:"id"`   // "local" or the configured peer name (slug)
	Name       string    `json:"name"` // display name: hostname for local, peer name for peers
	Local      bool      `json:"local"`
	Online     bool      `json:"online"`           // local is always true
	LastSeen   int64     `json:"lastSeen"`         // unix ms of the last snapshot received; 0 = never
	IntervalMs int64     `json:"intervalMs"`       // server's sample interval; 0 = unknown yet
	Host       *HostInfo `json:"host,omitempty"`   // nil until the peer's hello has been seen
	Latest     *Snapshot `json:"latest,omitempty"` // most recent snapshot, if any
	// RecentCPU holds the last ≤60 CPU usage percentages (oldest first,
	// downsampled from the ring) so overview sparklines render on first paint.
	RecentCPU []float64 `json:"recentCpu,omitempty"`
}
