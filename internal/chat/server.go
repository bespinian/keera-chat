// Package chat serves Keera Chat: the sign-in, the chat API and the page.
//
// Everything a person may do is decided by Keera Gateway. Chat signs them in
// through the gateway, holds the gateway's sign-in in a sealed cookie, and
// asks the gateway who they are, which keys they hold and which models those
// keys may call. It keeps no state of its own: conversations stay in the
// browser, and each message brings its conversation with it.
package chat

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bespinian/keera-chat/internal/gateway"
	"github.com/bespinian/keera-chat/internal/secret"
	"github.com/bespinian/keera-chat/internal/version"
	"github.com/bespinian/keera-chat/internal/webui"
)

type Options struct {
	// PublicURL is this service as a browser reaches it.
	PublicURL string
	// GatewayURL is the gateway as a browser reaches it: where a sign-in
	// starts, and what the account menu links to.
	GatewayURL    string
	SecureCookies bool
	MaxBodyBytes  int64
}

type Server struct {
	opts Options
	gw   *gateway.Client
	box  *secret.Box
	log  *slog.Logger

	who whoCache
}

func New(opts Options, gw *gateway.Client, box *secret.Box, log *slog.Logger) *Server {
	return &Server{opts: opts, gw: gw, box: box, log: log,
		who: whoCache{entries: map[string]whoEntry{}}}
}

// Handler routes every request. Paths that are not the API, the sign-in or a
// file get the page, which routes on the URL itself.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": version.String()})
	})

	mux.HandleFunc("GET /auth/config", s.authConfig)
	mux.HandleFunc("GET /auth/login", s.login)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("POST /auth/logout", s.logout)

	api := func(pattern string, h handler) { mux.Handle(pattern, s.signedIn(h)) }
	api("GET /api/me", s.me)
	api("POST /api/org", s.switchOrg)
	api("POST /api/chat", s.reply)

	// A typo in an API path is a JSON 404, not the page with a 200.
	notFound := func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "there is no "+r.Method+" "+r.URL.Path)
	}
	mux.HandleFunc("/api/", notFound)
	mux.HandleFunc("/auth/", notFound)
	mux.Handle("/", webui.Handler())

	return s.logged(sameOrigin(securityHeaders(mux)))
}

// ------------------------------------------------------------ the caller

// caller is a signed-in person, as the gateway knows them.
type caller struct {
	token string
	me    gateway.Me
}

type handler func(http.ResponseWriter, *http.Request, *caller)

// signedIn resolves the session cookie to a person, or answers 401. A
// sign-in the gateway no longer accepts clears the cookie, so the page goes
// back to the sign-in screen instead of failing on every click.
func (s *Server) signedIn(next handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.readSession(r)
		if !ok {
			signedOut(w)
			return
		}
		me, err := s.whoIs(r, sess.Token)
		if err != nil {
			s.gatewayFailed(w, r, err)
			return
		}
		next(w, r, &caller{token: sess.Token, me: me})
	})
}

// whoCache remembers for a minute who a token belongs to, so a page load does
// not ask the gateway once per request. A person turned off in the gateway is
// still refused by the gateway on their next message, and here within the
// minute.
type whoCache struct {
	mu      sync.Mutex
	entries map[string]whoEntry
}

type whoEntry struct {
	me gateway.Me
	at time.Time
}

const (
	whoTTL = time.Minute
	// whoSweepAt is how many people the cache holds before it drops the
	// stale ones.
	whoSweepAt = 4096
)

func (s *Server) whoIs(r *http.Request, token string) (gateway.Me, error) {
	key := tokenKey(token)
	now := time.Now()
	s.who.mu.Lock()
	e, ok := s.who.entries[key]
	s.who.mu.Unlock()
	if ok && now.Sub(e.at) < whoTTL {
		return e.me, nil
	}
	me, err := s.gw.Me(r.Context(), token)
	if err != nil {
		return me, err
	}
	s.who.mu.Lock()
	defer s.who.mu.Unlock()
	// It is swept as it grows rather than on a timer, since only use grows it.
	if len(s.who.entries) > whoSweepAt {
		for k, e := range s.who.entries {
			if now.Sub(e.at) >= whoTTL {
				delete(s.who.entries, k)
			}
		}
	}
	s.who.entries[key] = whoEntry{me: me, at: now}
	return me, nil
}

func (s *Server) forget(token string) {
	s.who.mu.Lock()
	delete(s.who.entries, tokenKey(token))
	s.who.mu.Unlock()
}

// ------------------------------------------------------------ middleware

// contentSecurityPolicy matches the gateway panel's: scripts and connections
// from this origin only, no framing. Images may be data: URLs, since that is
// how attached images travel.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"font-src 'self'; " +
	"connect-src 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'; " +
	"object-src 'none'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// sameOrigin refuses a state-changing request another site made the browser
// send. The session cookie is SameSite=Lax, which already keeps it off
// cross-site POSTs; this is the second lock. The page sends a header no plain
// form can, and a browser says where a request came from in Sec-Fetch-Site.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			site := r.Header.Get("Sec-Fetch-Site")
			if r.Header.Get("X-Keera-Chat") != "1" || (site != "" && site != "same-origin") {
				writeError(w, http.StatusForbidden, "cross_site",
					"this request did not come from Keera Chat; reload the page and try again")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// logged writes one line per request. The query is left out, because the
// sign-in callback carries a one-time code in it, and so is every body.
func (s *Server) logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" {
			return
		}
		level := slog.LevelDebug
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/auth/") {
			level = slog.LevelInfo
		}
		if rec.status >= 500 {
			level = slog.LevelWarn
		}
		s.log.Log(r.Context(), level, "request", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration", time.Since(start).Round(time.Millisecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Unwrap lets http.ResponseController reach the real writer, for Flush and
// for lifting the write deadline while a reply streams.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// ------------------------------------------------------------ answers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers in the gateway's envelope, so the page reads one shape
// whoever refused.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func signedOut(w http.ResponseWriter) {
	writeError(w, http.StatusUnauthorized, "signed_out", "you are signed out; sign in again")
}

// gatewayFailed passes on the gateway's refusal with its own status, code
// and words. Anything else is a failure here, logged and kept vague. A
// sign-in the gateway no longer accepts ends here too, even if the who-is
// cache still holds it.
func (s *Server) gatewayFailed(w http.ResponseWriter, r *http.Request, err error) {
	if gateway.IsUnauthorized(err) {
		if sess, ok := s.readSession(r); ok {
			s.forget(sess.Token)
		}
		s.clearSession(w)
		signedOut(w)
		return
	}
	if e, ok := errors.AsType[*gateway.Error](err); ok {
		code := e.Code
		if code == "" {
			code = "gateway"
		}
		writeError(w, e.Status, code, e.Error())
		return
	}
	if r.Context().Err() != nil {
		return
	}
	s.log.Warn("calling the gateway failed", "path", r.URL.Path, "error", err)
	writeError(w, http.StatusBadGateway, "gateway", gateway.NoAnswer)
}

func (s *Server) failed(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, "internal", "something went wrong in Keera Chat; try again")
}

// readJSON decodes a request body of at most the configured size into v. When
// it cannot, it answers the request itself and reports false.
func (s *Server) readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, s.opts.MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large",
				"this chat is too large to send; start a new chat, or attach fewer or smaller images")
			return false
		}
		writeError(w, http.StatusBadRequest, "bad_request", "the request could not be read: "+err.Error())
		return false
	}
	return true
}
