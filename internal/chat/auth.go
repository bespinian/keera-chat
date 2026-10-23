package chat

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/bespinian/keera-chat/internal/gateway"
)

// Signing in is the gateway's own sign-in, the one `keera login` uses, with
// this service in the place of the terminal.
//
// The browser goes to the gateway's /control/auth/login with a redirect back
// to /auth/callback here, a PKCE challenge and a state. After the person signs
// in there, through their identity provider or a passkey, the gateway sends
// the browser back with a one-time code. Chat redeems the code, with the
// verifier only it holds, for a gateway token that acts as that person.
//
// The token lives in a cookie sealed with the secret key: the browser carries
// it but cannot read it, and the page's scripts cannot even see it.

const (
	sessionCookie = "keera_chat"
	loginCookie   = "keera_chat_login"
	// errorCookie holds why a sign-in failed, until the sign-in screen
	// shows it.
	errorCookie = "keera_chat_error"

	// loginTTL is how long a sign-in may take, the same as the gateway
	// allows: people leave a consent screen open.
	loginTTL = 15 * time.Minute

	// errorTTL is long enough for the page to load and ask for the reason.
	errorTTL = time.Minute

	// callbackPath is where the gateway sends the browser back to. The
	// gateway accepts it only for the address it was told in KEERA_CHAT_URL,
	// or on a loopback address.
	callbackPath = "/auth/callback"
)

// session is what the session cookie holds, sealed.
type session struct {
	Token   string `json:"t"`
	Expires int64  `json:"x"`
}

// pending is what the login cookie holds, sealed, while the person is at the
// gateway.
type pending struct {
	State    string `json:"s"`
	Verifier string `json:"v"`
	Expires  int64  `json:"x"`
}

// authConfig tells the sign-in screen which ways in the gateway offers.
func (s *Server) authConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.gw.AuthConfig(r.Context())
	if err != nil {
		s.gatewayFailed(w, r, err)
		return
	}
	providers := []gateway.Provider{}
	if cfg.SSO {
		providers = append(providers, cfg.Providers...)
	}
	// Why the last sign-in failed is shown once. It is cleared only here, so
	// a gateway that did not answer above does not lose it.
	var signInError string
	if s.readSealed(r, errorCookie, &signInError) {
		s.clearCookie(w, errorCookie, "/auth")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"providers":     providers,
		"passkeys":      cfg.Passkeys,
		"gateway_url":   s.opts.GatewayURL,
		"sign_in_error": signInError,
	})
}

// login sends the browser to the gateway to sign in.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	p := pending{State: randomToken(), Verifier: randomToken(),
		Expires: time.Now().Add(loginTTL).Unix()}
	if err := s.setSealed(w, loginCookie, "/auth", p, loginTTL); err != nil {
		s.failed(w, r, err)
		return
	}
	q := url.Values{
		"cli_redirect":  {s.opts.PublicURL + callbackPath},
		"cli_challenge": {challenge(p.Verifier)},
		"cli_state":     {p.State},
	}
	// The gateway refuses to guess between several ways in, so the screen
	// names one.
	if provider := r.URL.Query().Get("provider"); provider != "" {
		q.Set("provider", provider)
	}
	http.Redirect(w, r, s.opts.GatewayURL+gateway.ControlPrefix+"/auth/login?"+q.Encode(), http.StatusFound)
}

// callback finishes a sign-in: the browser is back from the gateway with a
// one-time code.
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	var p pending
	ok := s.readSealed(r, loginCookie, &p)
	s.clearCookie(w, loginCookie, "/auth")
	q := r.URL.Query()
	switch {
	case !ok || time.Now().Unix() > p.Expires:
		s.failSignIn(w, r, "this sign-in took too long, or was started in another browser; try again")
		return
	// The state ties the code to the sign-in this browser started, so
	// nobody can send someone a link that signs them in as somebody else.
	case subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(p.State)) != 1:
		s.failSignIn(w, r, "this sign-in was not the one this browser started; try again")
		return
	case q.Get("code") == "":
		s.failSignIn(w, r, "the sign-in came back without a code; try again")
		return
	}

	tok, err := s.gw.Redeem(r.Context(), q.Get("code"), p.Verifier, "Keera Chat")
	if err != nil {
		s.failSignIn(w, r, err.Error())
		return
	}
	// whoIs checks the new token and caches who it is for the page's first
	// /api/me.
	if _, err := s.whoIs(r, tok.Token); err != nil {
		s.failSignIn(w, r, err.Error())
		return
	}
	ttl := time.Until(tok.ExpiresAt)
	if ttl <= 0 {
		s.failSignIn(w, r, "the gateway issued a sign-in that has already expired; try again")
		return
	}
	if err := s.setSealed(w, sessionCookie, "/", session{Token: tok.Token,
		Expires: tok.ExpiresAt.Unix()}, ttl); err != nil {
		s.failed(w, r, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// logout ends the sign-in at the gateway too, so the token in the cookie is
// worthless even if a copy of the cookie survives.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if sess, ok := s.readSession(r); ok {
		s.forget(sess.Token)
		if err := s.gw.Logout(r.Context(), sess.Token); err != nil && !gateway.IsUnauthorized(err) {
			s.log.Warn("the gateway did not confirm a sign-out", "error", err)
		}
	}
	s.clearSession(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// failSignIn sends the browser to the sign-in screen, with the reason in a
// sealed cookie rather than the URL. A reason in the URL would let anyone
// send a link that shows their own words on this sign-in screen.
func (s *Server) failSignIn(w http.ResponseWriter, r *http.Request, msg string) {
	if err := s.setSealed(w, errorCookie, "/auth", msg, errorTTL); err != nil {
		s.failed(w, r, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// ------------------------------------------------------------ cookies

func (s *Server) readSession(r *http.Request) (session, bool) {
	var sess session
	if !s.readSealed(r, sessionCookie, &sess) || sess.Token == "" ||
		time.Now().Unix() > sess.Expires {
		return session{}, false
	}
	return sess, true
}

func (s *Server) clearSession(w http.ResponseWriter) { s.clearCookie(w, sessionCookie, "/") }

// setSealed sets a cookie holding v, sealed under the cookie's name so one
// cookie's value cannot be passed off as another's.
func (s *Server) setSealed(w http.ResponseWriter, name, path string, v any, ttl time.Duration) error {
	plain, err := json.Marshal(v)
	if err != nil {
		return err
	}
	http.SetCookie(w, s.cookie(name, base64.RawURLEncoding.EncodeToString(
		s.box.Seal("cookie:"+name, plain)), path, int(ttl.Seconds())))
	return nil
}

func (s *Server) readSealed(r *http.Request, name string, v any) bool {
	c, err := r.Cookie(name)
	if err != nil || c.Value == "" {
		return false
	}
	sealed, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return false
	}
	plain, err := s.box.Open("cookie:"+name, sealed)
	if err != nil {
		return false
	}
	return json.Unmarshal(plain, v) == nil
}

func (s *Server) clearCookie(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, s.cookie(name, "", path, -1))
}

// cookie is every cookie chat sets: never readable by scripts, and Lax, so
// the browser sends it when the gateway redirects back here but not with
// another site's POST.
func (s *Server) cookie(name, value, path string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: path, MaxAge: maxAge,
		HttpOnly: true, Secure: s.opts.SecureCookies, SameSite: http.SameSiteLaxMode}
}

// ------------------------------------------------------------ secrets

// randomToken is 32 random bytes, base64url without padding: the shape the
// gateway expects of a PKCE verifier.
func randomToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// challenge is the PKCE S256 challenge for a verifier.
func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// tokenKey names a token in memory without keeping the token itself as a map
// key.
func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return string(sum[:])
}
