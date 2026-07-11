package server

import (
	"crypto/subtle"
	"fmt"
	"log"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// statusRecorder captures the response status for the request log. It
// forwards Flush so the SSE handler's http.Flusher assertion keeps working
// through the middleware chain, and implements Unwrap for
// http.ResponseController users.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	if sr.status == 0 {
		sr.status = code
	}
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if sr.status == 0 {
		sr.status = http.StatusOK
	}
	return sr.ResponseWriter.Write(b)
}

func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (sr *statusRecorder) Unwrap() http.ResponseWriter { return sr.ResponseWriter }

// withLogging logs one line per request: method, path, status, duration.
// The log line is deferred so aborted and panicking requests still log. The
// path is percent-decoded attacker input, so it is logged with %q: decoded
// newlines can't forge log lines and escape bytes can't inject ANSI into the
// terminal reading `docker logs`.
func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sr := &statusRecorder{ResponseWriter: w}
		defer func() {
			status := sr.status
			if status == 0 {
				status = http.StatusOK // handler wrote nothing; net/http sends 200
			}
			log.Printf("%s %q %d %s", r.Method, r.URL.Path, status, time.Since(start).Round(100*time.Microsecond))
		}()
		next.ServeHTTP(sr, r)
	})
}

// withHostCheck rejects requests whose Host header names anything other than
// this machine, blocking DNS rebinding (DESIGN.md §2): a hostile page can't
// point its own DNS name at this LAN address and read metrics as same-origin.
// IP literals, localhost (and *.localhost) and names in allowed
// (OWLWATCH_ALLOWED_HOSTS) pass; anything else gets 421 Misdirected Request.
func withHostCheck(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host, allowed) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusMisdirectedRequest)
			fmt.Fprintf(w, "unrecognized Host %q: rejected to block DNS rebinding; add the name to OWLWATCH_ALLOWED_HOSTS to accept it\n", r.Host)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostAllowed reports whether a Host header value names this service; any
// port suffix is ignored.
func hostAllowed(hostport string, allowed []string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	// A bare bracketed IPv6 literal ("[::1]", no port) fails SplitHostPort;
	// unbracket it by hand so ParseIP sees the address.
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if host == "" {
		return true // HTTP/1.0 clients may omit Host entirely
	}
	if net.ParseIP(host) != nil {
		return true // IP literals cannot be rebound
	}
	host = strings.ToLower(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	for _, name := range allowed {
		if strings.EqualFold(name, host) {
			return true
		}
	}
	return false
}

// withTokenAuth guards every /api/ route with the configured token
// (DESIGN.md §9.2), answering 401 JSON without it. /healthz and the static
// UI stay open: the UI shell is public, the data behind it is not, and
// `owlwatch -healthcheck` must keep working tokenless. An empty token
// disables the check (the v1 no-auth behavior).
func withTokenAuth(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPIPath(r.URL.Path) && !tokenMatches(r, token) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isAPIPath reports whether a request path is under the token-gated /api/
// tree. Bare /api (an unknown endpoint answered 404 by the UI handler) is
// gated too, so probing cannot tell gated routes from missing ones.
func isAPIPath(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/")
}

// tokenMatches reports whether the request presents the API token, either as
// `Authorization: Bearer <t>` or as a `?token=<t>` query parameter — the
// query form exists because EventSource cannot set headers (DESIGN.md §9.2).
// Both compares are constant-time; only the guess's length can leak, which
// is fine for a bearer token.
func tokenMatches(r *http.Request, token string) bool {
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok &&
		subtle.ConstantTimeCompare([]byte(bearer), []byte(token)) == 1 {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(token)) == 1
}

// withRecovery turns handler panics into 500s instead of killing the
// connection silently, and logs the stack. http.ErrAbortHandler is re-raised
// because it is net/http's own control-flow signal.
func withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			log.Printf("panic serving %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
			// Only send a 500 if the handler had not started responding.
			if sr, ok := w.(*statusRecorder); !ok || sr.status == 0 {
				writeJSONError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
