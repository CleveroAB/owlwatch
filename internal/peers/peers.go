// Package peers implements the hub side of federation: a Client keeps one
// SSE connection per configured peer (that peer's plain v1 /api/live), caches
// each peer's host info, sample interval and a short ring of recent
// snapshots, and fans live events out to subscribers without ever blocking a
// peer goroutine (same drop pattern as internal/collector). Metric history is
// never cached — it stays on the peers and History proxies range queries on
// demand.
package peers

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Sentinel errors returned (wrapped — test with errors.Is) by Client.History.
var (
	ErrUnknownPeer     = errors.New("unknown peer")
	ErrPeerUnavailable = errors.New("peer unavailable")
)

// Peer is one federation upstream parsed from OWLWATCH_PEERS.
type Peer struct {
	ID    string   // slug, validated per DESIGN.md §9.1 (the lowercased name)
	Name  string   // display name (the configured name, original casing)
	URL   *url.URL // base URL, no trailing slash
	Token string   // outgoing bearer token ("" = none)
}

// idPattern validates peer IDs (configured names after lowercasing).
var idPattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// reservedIDs are server ids claimed by the hub itself: "local" is the hub's
// own server and "overview" is the fleet stream route.
var reservedIDs = map[string]bool{"local": true, "overview": true}

// ParsePeers parses OWLWATCH_PEERS: comma-separated name=url pairs, where a
// url may carry a |token suffix that overrides defaultToken for that peer
// (an empty override, "url|", explicitly sends no token). Names are
// lowercased into IDs and must be unique, match [a-z0-9-]{1,32} and avoid the
// reserved ids "local" and "overview". URLs must be absolute http/https with
// no userinfo, path, query or fragment; one trailing "/" is tolerated and
// stripped.
// Returns nil, nil for empty input; anything invalid is an error — a fatal
// startup condition for the caller (fail fast, not silently).
func ParsePeers(env, defaultToken string) ([]Peer, error) {
	if strings.TrimSpace(env) == "" {
		return nil, nil
	}
	var peers []Peer
	seen := make(map[string]bool)
	for _, entry := range strings.Split(env, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue // tolerate a trailing comma
		}
		name, rest, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("peers: %q: want name=url", entry)
		}
		name = strings.TrimSpace(name)
		token := defaultToken
		urlStr, override, hasOverride := strings.Cut(strings.TrimSpace(rest), "|")
		if hasOverride {
			token = strings.TrimSpace(override)
		}
		id := strings.ToLower(name)
		if !idPattern.MatchString(id) {
			return nil, fmt.Errorf("peers: invalid name %q: must match [a-z0-9-]{1,32} after lowercasing", name)
		}
		if reservedIDs[id] {
			return nil, fmt.Errorf("peers: name %q is reserved", name)
		}
		if seen[id] {
			return nil, fmt.Errorf("peers: duplicate name %q", id)
		}
		seen[id] = true
		u, err := parseBaseURL(strings.TrimSpace(urlStr))
		if err != nil {
			return nil, fmt.Errorf("peers: %s: %w", name, err)
		}
		peers = append(peers, Peer{ID: id, Name: name, URL: u, Token: token})
	}
	if len(peers) == 0 {
		return nil, nil // e.g. OWLWATCH_PEERS="," — nothing actually configured
	}
	return peers, nil
}

// parseBaseURL validates a peer base URL: absolute http/https, host only —
// no userinfo, path, query or fragment (one trailing "/" is stripped first).
func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSuffix(raw, "/"))
	if err != nil {
		return nil, fmt.Errorf("invalid url %q: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("invalid url %q: scheme must be http or https", raw)
	}
	if u.User != nil {
		// Userinfo would be silently ignored by the client and its password
		// would end up in logs — u.Redacted() keeps it out of this error too.
		return nil, fmt.Errorf("invalid url %q: must not contain userinfo; use the |token suffix to send a bearer token", u.Redacted())
	}
	if u.Host == "" {
		return nil, fmt.Errorf("invalid url %q: missing host", raw)
	}
	if u.Opaque != "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid url %q: must not carry a path, query or fragment", raw)
	}
	return u, nil
}
