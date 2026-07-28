package server

import (
	"log"
	"net/http"
)

// alertSender is cfg.Alerts behind a test seam (same pattern as peerSource):
// tests inject a fake so no test ever opens an SMTP connection.
type alertSender interface {
	SendTest() error
}

// handleAlerts reports whether email alerting is configured, so the UI can
// decide to render the test button at all.
func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": s.alerts != nil})
}

// handleAlertsTest sends the hard-coded test email over the configured SMTP
// connection. The send is synchronous (bounded by the mailer's connection
// deadline) so the response reflects the actual delivery attempt.
func (s *Server) handleAlertsTest(w http.ResponseWriter, r *http.Request) {
	if s.alerts == nil {
		writeJSONError(w, http.StatusConflict, "email alerts are not configured on this server")
		return
	}
	if err := s.alerts.SendTest(); err != nil {
		log.Printf("alerts: test email: %v", err)
		writeJSONError(w, http.StatusBadGateway, "sending the test email failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
