package server

import "net/http"

// errActionsNeedToken is the refusal for every mutating route on an instance
// without OWLWATCH_TOKEN: the bearer token is the only thing standing between
// the port and a restart or an email, so no token means no actions.
const errActionsNeedToken = "this action requires OWLWATCH_TOKEN to be set on the server"

// actionsResponse tells the UI which mutating controls this instance will
// accept, so it can hide the ones that would be refused.
type actionsResponse struct {
	Restart   bool `json:"restart"`
	TestEmail bool `json:"testEmail"`
}

func (s *Server) handleActions(w http.ResponseWriter, r *http.Request) {
	token := s.cfg.Token != ""
	writeJSON(w, http.StatusOK, actionsResponse{
		Restart:   token && s.cfg.Reboot != nil,
		TestEmail: token && s.alerts != nil,
	})
}

// requireToken refuses a mutating route unless OWLWATCH_TOKEN is configured.
// withTokenAuth has already checked the request's bearer token by the time
// this runs; this only closes the unauthenticated deployment.
func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token == "" {
			writeJSONError(w, http.StatusForbidden, errActionsNeedToken)
			return
		}
		next(w, r)
	}
}
