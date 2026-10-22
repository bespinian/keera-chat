// Package gatewaytest is a stand-in Keera Gateway: the routes chat calls,
// with one person in two organisations, a few keys and models that answer
// from a script. The tests drive chat against it.
//
// It checks what the real gateway checks where chat could get it wrong: the
// PKCE verifier, the bearer token and the key id. It records the client name,
// so a test can check that too.
package gatewaytest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	UserID = "user_ada"
	OrgID  = "org_example"
	KeyID  = "key_laptop"
)

type Gateway struct {
	// Delay is the pause between streamed words, so a reply takes a while.
	Delay time.Duration

	mu         sync.Mutex
	codes      map[string]string // one-time code → PKCE challenge
	tokens     map[string]bool
	orgID      string
	lastChat   map[string]any
	lastClient string
	chats      int
	streaming  int
}

func New() *Gateway {
	return &Gateway{codes: map[string]string{}, tokens: map[string]bool{}, orgID: OrgID}
}

// LastChat is the body of the last chat request, as the model would get it.
func (g *Gateway) LastChat() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastChat
}

// LastClient is the X-Keera-Client header of the last chat request.
func (g *Gateway) LastClient() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastClient
}

// Chats counts the chat requests that reached the model.
func (g *Gateway) Chats() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.chats
}

// Streaming counts the replies the model is still writing, so a test can see
// that stopping a reply ends the model's work.
func (g *Gateway) Streaming() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.streaming
}

// Revoke ends every sign-in, as signing out everywhere would.
func (g *Gateway) Revoke() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tokens = map[string]bool{}
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /control/auth/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"sso":       true,
			"providers": []map[string]any{{"name": "example", "label": "Example ID"}},
			"passkeys":  true,
		})
	})
	mux.HandleFunc("GET /control/auth/login", g.login)
	mux.HandleFunc("POST /control/auth/cli/token", g.redeem)
	mux.HandleFunc("POST /control/auth/logout", g.authed(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		delete(g.tokens, bearer(r))
		g.mu.Unlock()
		writeJSON(w, 200, map[string]any{})
	}))
	mux.HandleFunc("GET /control/v1/me", g.authed(g.me))
	mux.HandleFunc("POST /control/v1/me/org", g.authed(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			OrgID string `json:"org_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		g.mu.Lock()
		g.orgID = in.OrgID
		g.mu.Unlock()
		writeJSON(w, 200, map[string]any{"ok": true})
	}))
	mux.HandleFunc("GET /control/v1/access", g.authed(g.access))
	mux.HandleFunc("GET /control/v1/models", g.authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"data": []map[string]any{
			{"alias": "keera-speed", "kind": "chat", "enabled": true, "display_name": "Keera Speed",
				"description": "Quick answers for everyday questions", "max_context_tokens": 131072},
			{"alias": "keera-think", "kind": "chat", "enabled": true, "display_name": "Keera Think",
				"description": "Thinks first, for harder questions", "max_context_tokens": 262144},
			{"alias": "keera-embed", "kind": "embedding", "enabled": true},
			{"alias": "claude-plan", "kind": "chat", "enabled": true, "subscription": true},
		}})
	}))
	mux.HandleFunc("GET /control/v1/routers", g.authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"data": []map[string]any{
			{"alias": "keera-auto", "description": "Picks Speed or Think for each message"},
		}})
	}))
	mux.HandleFunc("POST /control/v1/playground/chat", g.authed(g.chat))
	return mux
}

// login stands in for the whole sign-in at an identity provider: it sends the
// browser straight back with a code, as the gateway does once the person has
// signed in.
func (g *Gateway) login(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	back, err := url.Parse(q.Get("cli_redirect"))
	if err != nil || back.Host == "" || q.Get("cli_challenge") == "" || q.Get("cli_state") == "" {
		http.Error(w, "a sign-in needs cli_redirect, cli_challenge and cli_state", 400)
		return
	}
	code := random()
	g.mu.Lock()
	g.codes[code] = q.Get("cli_challenge")
	g.mu.Unlock()
	back.RawQuery = url.Values{"code": {code}, "state": {q.Get("cli_state")}}.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (g *Gateway) redeem(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code     string `json:"code"`
		Verifier string `json:"verifier"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	g.mu.Lock()
	defer g.mu.Unlock()
	challenge, ok := g.codes[in.Code]
	delete(g.codes, in.Code)
	sum := sha256.Sum256([]byte(in.Verifier))
	if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
		writeError(w, 401, "invalid_code", "this sign-in has expired or was already completed")
		return
	}
	token := "keera_cli_" + random()
	g.tokens[token] = true
	writeJSON(w, 200, map[string]any{"token": token, "expires_at": time.Now().Add(30 * 24 * time.Hour)})
}

func (g *Gateway) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		ok := g.tokens[bearer(r)]
		g.mu.Unlock()
		if !ok {
			writeError(w, 401, "unauthenticated", "sign in, or send the operator key")
			return
		}
		next(w, r)
	}
}

func (g *Gateway) me(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	org := g.orgID
	g.mu.Unlock()
	names := map[string]string{OrgID: "Example AG", "org_lab": "Example Lab"}
	writeJSON(w, 200, map[string]any{
		"user_id": UserID, "email": "ada@example.ch",
		"name": "Ada Lovelace", "role": "member", "org_id": org, "org_name": names[org],
		"orgs": []map[string]any{
			{"org_id": OrgID, "org_name": "Example AG", "role": "member"},
			{"org_id": "org_lab", "org_name": "Example Lab", "role": "member"},
			{"org_id": "org_other", "org_name": "Invited Corp", "role": "member", "invited": true},
		},
	})
}

func (g *Gateway) access(w http.ResponseWriter, r *http.Request) {
	resets := time.Date(time.Now().Year(), time.Now().Month()+1, 1, 0, 0, 0, 0, time.UTC)
	writeJSON(w, 200, map[string]any{"keys": []map[string]any{
		{"id": KeyID, "org_id": OrgID, "name": "Laptop", "prefix": "kg_ab12", "kind": "standard",
			"state": "active", "project_name": "Research",
			"allowed_models": []string{"keera-speed", "keera-think", "keera-auto", "keera-embed"},
			"scopes": []map[string]any{
				{"type": "org", "name": "Example AG", "budget_micros": 1_000_000_000,
					"spend_micros": 120_000_000, "period": "month", "resets_at": resets},
				{"type": "project", "name": "Research", "budget_micros": 50_000_000,
					"spend_micros": 12_340_000, "period": "month", "resets_at": resets},
			}},
		{"id": "key_pending", "org_id": OrgID, "name": "Phone", "kind": "standard", "state": "pending"},
		{"id": "key_sub", "org_id": OrgID, "name": "Claude", "kind": "subscription", "state": "active"},
	}})
}

// chat answers from a script, chosen by what the last message says:
//
//   - "budget" is refused as a spent budget would be,
//   - "too long" is refused as too long for the model's context, in the
//     gateway's words for agents,
//   - "overloaded" is refused by the model server, in vLLM's words, which
//     the gateway passes on,
//   - "broken" fails partway through the reply, as a model server can,
//   - "slow" streams for a long time, to be stopped,
//   - anything else gets a reply that shows off the formatting, with the
//     message quoted back, and keera-think thinks first.
func (g *Gateway) chat(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("key_id") != KeyID {
		writeError(w, 404, "not_found", "not found")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	g.mu.Lock()
	g.lastChat, g.lastClient = body, r.Header.Get("X-Keera-Client")
	g.mu.Unlock()

	model, _ := body["model"].(string)
	last := lastText(body)
	switch {
	case model != "keera-speed" && model != "keera-think" && model != "keera-auto":
		writeError(w, 403, "model_not_allowed", "this key may not use "+model)
		return
	case strings.Contains(strings.ToLower(last), "budget"):
		writeError(w, 429, "budget_exceeded",
			"the monthly budget of project Research is spent; it starts again on 1 November")
		return
	case strings.Contains(strings.ToLower(last), "too long"):
		writeJSON(w, 400, map[string]any{"error": map[string]any{
			"message": "prompt is too long: 140000 tokens > 131072 maximum. That is the least this " +
				"request can be, and '" + model + "' holds no more; compact the conversation or start a new one",
			"type": "invalid_request_error", "code": "context_length_exceeded",
		}})
		return
	case strings.Contains(strings.ToLower(last), "overloaded"):
		writeJSON(w, 503, map[string]any{"error": map[string]any{
			"message": "The engine is currently overloaded, please try again later.",
			"type":    "ServiceUnavailableError", "code": 503,
		}})
		return
	}
	g.mu.Lock()
	g.chats++
	g.streaming++
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.streaming--
		g.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	flush := func() { _ = http.NewResponseController(w).Flush() }
	send := func(delta map[string]any, finish any) bool {
		chunk := map[string]any{"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}}}
		b, _ := json.Marshal(chunk)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		flush()
		if g.Delay > 0 {
			select {
			case <-time.After(g.Delay):
			case <-r.Context().Done():
				return false
			}
		}
		return r.Context().Err() == nil
	}

	words := func(field, text string) bool {
		for _, w := range strings.SplitAfter(text, " ") {
			if !send(map[string]any{field: w}, nil) {
				return false
			}
		}
		return true
	}

	if strings.Contains(strings.ToLower(last), "broken") {
		if words("content", "Let me think about ") {
			b, _ := json.Marshal(map[string]any{"error": map[string]any{
				"message": "CUDA error: an illegal memory access was encountered", "code": 500,
			}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		}
		return
	}
	if strings.Contains(strings.ToLower(last), "slow") {
		for i := 0; r.Context().Err() == nil && i < 100000; i++ {
			if !send(map[string]any{"content": "and on "}, nil) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		return
	}
	if model == "keera-think" && !words("reasoning", "The person asked: "+oneLine(last)+
		". A short answer with an example should do. ") {
		return
	}
	if !words("content", reply(last)) {
		return
	}
	send(map[string]any{}, "stop")
	b, _ := json.Marshal(map[string]any{"choices": []any{},
		"usage": map[string]int{"prompt_tokens": 42 * len(body["messages"].([]any)), "completion_tokens": 128}})
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
	flush()
}

func reply(last string) string {
	return "Here is what you wrote, back to you:\n\n> " + oneLine(last) + "\n\n" +
		"## A few things worth knowing\n\n" +
		"1. **Replies stream** as the model writes them.\n" +
		"2. Inline `code` and [links](https://keera.ch) work.\n" +
		"3. Lists can nest:\n   - like this\n   - and this\n\n" +
		"| Model | Good for |\n|---|---|\n| Keera Speed | everyday questions |\n| Keera Think | harder problems |\n\n" +
		"```go\nfunc hello() string {\n\treturn \"Grüezi\"\n}\n```\n\n" +
		"Anything else?"
}

func lastText(body map[string]any) string {
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		return ""
	}
	m, _ := msgs[len(msgs)-1].(map[string]any)
	switch c := m["content"].(type) {
	case string:
		return c
	case []any:
		for _, p := range c {
			if part, _ := p.(map[string]any); part["type"] == "text" {
				s, _ := part["text"].(string)
				return s
			}
		}
		return "(an image)"
	}
	return ""
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func random() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg, "type": "error"}})
}
