package server

import (
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
