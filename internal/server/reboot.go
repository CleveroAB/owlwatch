package server

import (
	"log"
	"net/http"
	"time"
)

const rebootActionHeader = "X-Owlwatch-Action"

// handleReboot accepts a local process reboot. The custom header makes the
// mutating endpoint unavailable to cross-origin HTML forms when token auth is
// disabled; browsers cannot attach it without a CORS preflight, which owlwatch
// does not permit.
func (s *Server) handleReboot(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(rebootActionHeader) != "reboot" {
		writeJSONError(w, http.StatusForbidden, "reboot confirmation header is required")
		return
	}
	if s.cfg.Reboot == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "server reboot is unavailable")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})

	// Let net/http flush the accepted response before cancellation begins.
	// sync.Once makes concurrent requests collapse into one graceful reboot.
	s.reboot.Do(func() {
		time.AfterFunc(100*time.Millisecond, func() {
			log.Printf("reboot requested through the API")
			s.cfg.Reboot()
		})
	})
}
