// Package chat serves Keera Chat: the sign-in, the chat API and the page.
//
// Keera Gateway decides what a person may do. Chat keeps no state:
// conversations stay in the browser and each message carries its history.
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
	// PublicURL and GatewayURL are as a browser reaches them.
	PublicURL     string
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
	return &Server{
		opts: opts, gw: gw, box: box, log: log,
		who: whoCache{entries: map[string]whoEntry{}},
	}
}

// Handler routes every request. Unknown paths get the page, which routes
// itself.
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

	// Unknown API paths get a JSON 404, not the page.
	notFound := func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "there is no "+r.Method+" "+r.URL.Path)
	}
	mux.HandleFunc("/api/", notFound)
	mux.HandleFunc("/auth/", notFound)
	mux.Handle("/", webui.Handler())

	return s.logged(sameOrigin(securityHeaders(mux)))
}

// ------------------------------------------------------------ the caller

type caller struct {
	token string
	me    gateway.Me
}

type handler func(http.ResponseWriter, *http.Request, *caller)

// signedIn resolves the session cookie to a person, or answers 401.
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

// whoCache remembers who a token belongs to for a minute, so a page load
// does not ask the gateway on every request.
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
	// whoSweepAt is the size at which stale entries are dropped.
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
	// Swept as it grows, not on a timer.
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

// contentSecurityPolicy matches the gateway panel's, but allows data: images
// for attachments.
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

// sameOrigin refuses cross-site writes, as a second lock after SameSite=Lax.
// The page sends a header no plain form can, and browsers set
// Sec-Fetch-Site.
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

// logged writes one line per request. It leaves out the query, which can
// hold a one-time code, and the body.
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

// Unwrap lets http.ResponseController reach the real writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// ------------------------------------------------------------ answers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError uses the gateway's error shape, so the page reads one shape.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func signedOut(w http.ResponseWriter) {
	writeError(w, http.StatusUnauthorized, "signed_out", "you are signed out; sign in again")
}

// gatewayFailed passes on the gateway's refusal as it is. Other errors are
// logged and kept vague. A rejected sign-in also ends the session here.
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

// readJSON decodes the body into v. On failure it answers and returns false.
func (s *Server) readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, s.opts.MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", gateway.TooLarge)
			return false
		}
		writeError(w, http.StatusBadRequest, "bad_request", "the request could not be read: "+err.Error())
		return false
	}
	return true
}
