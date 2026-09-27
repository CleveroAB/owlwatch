package server

import (
	"log"
	"net/http"
	"time"
)

// handleReboot accepts a local process restart. It is only reachable through
// requireToken, so the request already carried a valid bearer token — which a
// cross-origin page cannot attach, making a separate CSRF guard unnecessary.
func (s *Server) handleReboot(w http.ResponseWriter, r *http.Request) {
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
