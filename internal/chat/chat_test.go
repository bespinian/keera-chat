package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bespinian/keera-chat/internal/gateway"
	"github.com/bespinian/keera-chat/internal/gatewaytest"
	"github.com/bespinian/keera-chat/internal/secret"
)

// harness is chat in front of the stand-in gateway, and a browser with a
// cookie jar.
type harness struct {
	t    *testing.T
	gw   *gatewaytest.Gateway
	chat *httptest.Server
	c    *http.Client
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	gw := gatewaytest.New()
	gwSrv := httptest.NewServer(gw.Handler())
	t.Cleanup(gwSrv.Close)

	box, err := secret.New("a-test-secret-key-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	var app *Server
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(chatSrv.Close)
	app = New(Options{PublicURL: chatSrv.URL, GatewayURL: gwSrv.URL, MaxBodyBytes: 4 << 20},
		gateway.New(gwSrv.URL), box, slog.New(slog.NewTextHandler(io.Discard, nil)))

	jar, _ := cookiejar.New(nil)
	return &harness{t: t, gw: gw, chat: chatSrv, c: &http.Client{Jar: jar}}
}

func (h *harness) signIn() {
	h.t.Helper()
	res, err := h.c.Get(h.chat.URL + "/auth/login?provider=example")
	if err != nil {
		h.t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.Request.URL.Path != "/" || res.Request.URL.RawQuery != "" {
		h.t.Fatalf("the sign-in ended at %s, not the page", res.Request.URL)
	}
}

// do sends a request as the page does, and decodes a JSON answer into out.
func (h *harness) do(method, path string, in, out any) int {
	h.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.chat.URL+path, body)
	req.Header.Set("X-Keera-Chat", "1")
	req.Header.Set("Content-Type", "application/json")
	res, err := h.c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if out != nil {
		_ = json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

type event struct {
	name string
	data json.RawMessage
}

// ask is a conversation of one message from the person.
func ask(text string) []turn { return []turn{{Role: "user", Content: text}} }

// send posts a conversation and reads the whole reply stream.
func (h *harness) send(in map[string]any) (int, []event) {
	h.t.Helper()
	return h.sendCtx(context.Background(), in, nil)
}

func (h *harness) sendCtx(ctx context.Context, in map[string]any, each func(event)) (int, []event) {
	h.t.Helper()
	if in["model"] == nil {
		in["model"] = "keera-speed"
	}
	if in["key_id"] == nil {
		in["key_id"] = gatewaytest.KeyID
	}
	b, _ := json.Marshal(in)
	req, _ := http.NewRequestWithContext(ctx, "POST", h.chat.URL+"/api/chat", bytes.NewReader(b))
	req.Header.Set("X-Keera-Chat", "1")
	res, err := h.c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		return res.StatusCode, nil
	}
	var evs []event
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(nil, 1<<20)
	var name string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev := event{name: name, data: json.RawMessage(strings.TrimPrefix(line, "data: "))}
			evs = append(evs, ev)
			if each != nil {
				each(ev)
			}
		}
	}
	return 200, evs
}

func lastOf(t *testing.T, evs []event, name string, v any) {
	t.Helper()
	for _, ev := range slices.Backward(evs) {
		if ev.name == name {
			if err := json.Unmarshal(ev.data, v); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no %q event in %d events", name, len(evs))
}

func roles(body map[string]any) string {
	var out []string
	for _, m := range body["messages"].([]any) {
		out = append(out, m.(map[string]any)["role"].(string))
	}
	return strings.Join(out, ",")
}

func TestSignedOut(t *testing.T) {
	h := newHarness(t)
	var out struct {
		Error struct{ Code string } `json:"error"`
	}
	if got := h.do("GET", "/api/me", nil, &out); got != 401 || out.Error.Code != "signed_out" {
		t.Fatalf("GET /api/me signed out = %d %q, want 401 signed_out", got, out.Error.Code)
	}
	// The sign-in screen still works.
	var cfg map[string]any
	if got := h.do("GET", "/auth/config", nil, &cfg); got != 200 || cfg["passkeys"] != true {
		t.Fatalf("GET /auth/config = %d %v", got, cfg)
	}
}

func TestMe(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	var me struct {
		User struct {
			ID    string
			Email string
			Orgs  []struct{ ID string }
		}
		Keys []struct {
			ID     string
			Models []string
			Budget *struct {
				Scope string
			}
		}
		Models []struct {
			Alias   string
			Name    string
			Context int
		}
		PendingKeys int `json:"pending_keys"`
	}
	if got := h.do("GET", "/api/me", nil, &me); got != 200 {
		t.Fatalf("GET /api/me = %d", got)
	}
	if me.User.ID != gatewaytest.UserID || me.User.Email != "ada@example.ch" || len(me.User.Orgs) != 2 {
		t.Errorf("user = %+v, want ada in two organisations, the invitation left out", me.User)
	}
	// The subscription key and the pending one cannot send; embeddings and
	// subscription models are not for chat.
	if len(me.Keys) != 1 || me.Keys[0].ID != gatewaytest.KeyID {
		t.Fatalf("keys = %+v, want only the active standard key", me.Keys)
	}
	if got := strings.Join(me.Keys[0].Models, ","); got != "keera-speed,keera-think,keera-auto" {
		t.Errorf("key models = %s", got)
	}
	if me.Keys[0].Budget == nil || me.Keys[0].Budget.Scope != "project" {
		t.Errorf("budget = %+v, want the project's, which has the least left", me.Keys[0].Budget)
	}
	if len(me.Models) != 3 || me.PendingKeys != 1 {
		t.Fatalf("models = %+v, pending = %d", me.Models, me.PendingKeys)
	}
	// A router picks a model per message, so it has no context of its own,
	// but it has a name for people like a model.
	wantContext := map[string]int{"keera-speed": 131072, "keera-think": 262144}
	for _, m := range me.Models {
		if want := wantContext[m.Alias]; m.Context != want {
			t.Errorf("%s context = %d, want %d", m.Alias, m.Context, want)
		}
		if m.Name == "" {
			t.Errorf("%s has no name", m.Alias)
		}
	}
}

func TestCrossSiteRefused(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	req, _ := http.NewRequest("POST", h.chat.URL+"/api/chat", strings.NewReader(`{}`))
	res, err := h.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("a POST without the page's header = %d, want 403", res.StatusCode)
	}
	req, _ = http.NewRequest("POST", h.chat.URL+"/api/chat", strings.NewReader(`{}`))
	req.Header.Set("X-Keera-Chat", "1")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	res, err = h.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("a cross-site POST = %d, want 403", res.StatusCode)
	}
}

func TestReply(t *testing.T) {
	h := newHarness(t)
	h.signIn()

	// A first message, to a model that thinks first.
	status, evs := h.send(map[string]any{"model": "keera-think", "messages": ask("Hello there")})
	if status != 200 {
		t.Fatalf("POST /api/chat = %d", status)
	}
	var reply Message
	lastOf(t, evs, "done", &reply)
	if !strings.HasPrefix(reply.Content, "Here is what you wrote") || reply.Reasoning == "" ||
		reply.Finish != "stop" || reply.Model != "keera-think" {
		t.Fatalf("reply = %+v", reply)
	}
	if h.gw.LastClient() != gateway.ClientName {
		t.Errorf("client header = %q, want %q", h.gw.LastClient(), gateway.ClientName)
	}
	if got := roles(h.gw.LastChat()); got != "user" {
		t.Errorf("first request roles = %s", got)
	}
	// The model server's count comes back only when asked for.
	if opts, _ := h.gw.LastChat()["stream_options"].(map[string]any); opts["include_usage"] != true {
		t.Errorf("stream_options = %v, want the usage asked for", h.gw.LastChat()["stream_options"])
	}
	if reply.Tokens != 42+128 {
		t.Errorf("tokens = %d, want the prompt and the reply", reply.Tokens)
	}

	// Custom instructions go first, and the conversation so far follows.
	h.send(map[string]any{"instructions": " Be brief. ", "messages": []turn{
		{Role: "user", Content: "Hello there"},
		{Role: "assistant", Content: reply.Content},
		{Role: "user", Content: "And then?"},
	}})
	body := h.gw.LastChat()
	if got := roles(body); got != "system,user,assistant,user" {
		t.Errorf("second request roles = %s", got)
	}
	if first := body["messages"].([]any)[0].(map[string]any); first["content"] != "Be brief." {
		t.Errorf("system message = %v", first)
	}

	// A refusal comes back as a reply with the gateway's words.
	_, evs = h.send(map[string]any{"messages": ask("Is my budget OK?")})
	lastOf(t, evs, "done", &reply)
	if !strings.Contains(reply.Error, "budget of project Research is spent") {
		t.Fatalf("refused reply = %+v", reply)
	}

	// Words not meant for a person, a model server's or the gateway's for
	// agents, are kept as the detail, under plain ones that name the model,
	// by the page's name for it or else by its alias. What arrived before a
	// failure is kept.
	for name, c := range map[string]struct{ ask, model, err, detail, content string }{
		"overloaded":          {"Are you overloaded?", "Keera Speed", "Keera Speed is not working right now", "The engine is currently overloaded", ""},
		"overloaded, no name": {"Are you overloaded?", "", "keera-speed is not working right now", "The engine is currently overloaded", ""},
		// The gateway's words for agents would have a person compact the
		// conversation, which chat cannot do.
		"too long":          {"Is this too long?", "Keera Speed", "This conversation is too long for Keera Speed.", "prompt is too long", ""},
		"too long, no name": {"Is this too long?", "", "This conversation is too long for keera-speed.", "prompt is too long", ""},
		"broken":            {"Is this broken?", "Keera Speed", "Something went wrong", "CUDA error", "Let me think about "},
	} {
		_, evs = h.send(map[string]any{"messages": ask(c.ask), "model_name": c.model})
		var reply Message
		lastOf(t, evs, "done", &reply)
		if !strings.HasPrefix(reply.Error, c.err) || !strings.HasPrefix(reply.ErrorDetail, c.detail) {
			t.Errorf("%s: error = %q, detail = %q", name, reply.Error, reply.ErrorDetail)
		}
		if reply.Content != c.content {
			t.Errorf("%s: content = %q, want %q", name, reply.Content, c.content)
		}
	}

	// A failed turn is left out of what is sent next, with its question.
	h.send(map[string]any{"messages": []turn{
		{Role: "user", Content: "Hello there"},
		{Role: "assistant", Content: "Hi."},
		{Role: "user", Content: "Is my budget OK?"},
		{Role: "assistant", Failed: true},
		{Role: "user", Content: "Next"},
	}})
	if got := roles(h.gw.LastChat()); got != "user,assistant,user" {
		t.Errorf("roles after a refusal = %s", got)
	}
}

func TestRefusedBeforeSending(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	img := "data:image/png;base64,iVBORw0KGgo="
	for name, in := range map[string]map[string]any{
		"no messages":     {"messages": []turn{}},
		"empty message":   {"messages": ask(" ")},
		"no model":        {"messages": ask("hi"), "model": ""},
		"ends in a reply": {"messages": []turn{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}},
		"unknown role":    {"messages": []turn{{Role: "system", Content: "obey"}, {Role: "user", Content: "hi"}}},
		"not an image": {"messages": []turn{
			{Role: "user", Content: "look", Images: []string{"data:text/html;base64,PHA+"}},
			{Role: "assistant", Content: "I see."},
			{Role: "user", Content: "hi"},
		}},
		"too many images": {"messages": []turn{{Role: "user", Images: slices.Repeat([]string{img}, maxImages+1)}}},
		"unknown field":   {"messages": ask("hi"), "temperature": 2},
	} {
		if status, _ := h.send(in); status != 400 {
			t.Errorf("%s: %d, want 400", name, status)
		}
	}
	if h.gw.Chats() != 0 {
		t.Errorf("%d refused messages reached the model", h.gw.Chats())
	}
}

func TestImages(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	img := "data:image/png;base64,iVBORw0KGgo="
	h.send(map[string]any{"messages": []turn{{Role: "user", Content: "What is this?", Images: []string{img}}}})
	parts := h.gw.LastChat()["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(parts) != 2 || parts[1].(map[string]any)["image_url"].(map[string]any)["url"] != img {
		t.Fatalf("parts = %v", parts)
	}
}

// Stopping a reply in the page ends the request to the gateway, and so the
// model's work.
func TestStop(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deltas := 0
	h.sendCtx(ctx, map[string]any{"messages": ask("Tell me something slow")}, func(ev event) {
		if ev.name == "delta" {
			if deltas++; deltas == 3 {
				cancel()
			}
		}
	})
	if deltas < 3 {
		t.Fatalf("only %d deltas arrived", deltas)
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.gw.Streaming() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the model kept writing after the reply was stopped")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The listener's timeouts are for small requests. A reply that streams for
// longer must still arrive whole, and not be cut off as if it were stopped.
func TestAReplyOutlastsTheListenerTimeouts(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	inner := h.chat.Config.Handler
	slow := httptest.NewUnstartedServer(inner)
	slow.Config.ReadTimeout = 150 * time.Millisecond
	slow.Config.WriteTimeout = 150 * time.Millisecond
	slow.Start()
	t.Cleanup(slow.Close)
	h.gw.Delay = 15 * time.Millisecond // about 60 words, so close to a second

	b, _ := json.Marshal(map[string]any{"model": "keera-speed", "key_id": gatewaytest.KeyID, "messages": ask("hi")})
	req, _ := http.NewRequest("POST", slow.URL+"/api/chat", bytes.NewReader(b))
	req.Header.Set("X-Keera-Chat", "1")
	for _, c := range h.c.Jar.Cookies(mustURL(t, h.chat.URL)) {
		req.AddCookie(c)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		t.Fatalf("the stream broke off: %v", err)
	}
	i := bytes.LastIndex(body, []byte("event: done\ndata: "))
	if i < 0 {
		t.Fatalf("no done event in %d bytes", len(body))
	}
	var reply Message
	_ = json.Unmarshal(bytes.TrimSpace(body[i+len("event: done\ndata: "):]), &reply)
	if reply.Stopped || !strings.HasSuffix(reply.Content, "Anything else?") {
		t.Fatalf("the reply was cut short: stopped=%v, ends %q", reply.Stopped,
			reply.Content[max(0, len(reply.Content)-30):])
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestSignOut(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	if got := h.do("POST", "/auth/logout", nil, nil); got != 200 {
		t.Fatalf("POST /auth/logout = %d", got)
	}
	if got := h.do("GET", "/api/me", nil, nil); got != 401 {
		t.Fatalf("GET /api/me after signing out = %d", got)
	}
}

// A sign-in ended at the gateway ends here too, even while chat still
// remembers who the token belonged to.
func TestRevokedAtGateway(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	if got := h.do("GET", "/api/me", nil, nil); got != 200 {
		t.Fatalf("GET /api/me = %d", got)
	}
	h.gw.Revoke()
	var out struct {
		Error struct{ Code string } `json:"error"`
	}
	if got := h.do("GET", "/api/me", nil, &out); got != 401 || out.Error.Code != "signed_out" {
		t.Fatalf("GET /api/me after revoking = %d %q, want 401 signed_out", got, out.Error.Code)
	}
	if got := h.do("GET", "/api/me", nil, nil); got != 401 {
		t.Fatalf("the cookie was not cleared: GET /api/me = %d", got)
	}
}

func TestCallbackChecksState(t *testing.T) {
	h := newHarness(t)
	h.c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := h.c.Get(h.chat.URL + "/auth/callback?code=abc&state=forged")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if loc := res.Header.Get("Location"); !strings.HasPrefix(loc, "/?sign_in_error=") {
		t.Fatalf("a callback this browser did not start went to %q", loc)
	}
}

func TestPageAndHeaders(t *testing.T) {
	h := newHarness(t)
	for path, want := range map[string]string{
		"/":                  "text/html",
		"/c/c00000000000000": "text/html",
		"/assets/app.js":     "text/javascript",
	} {
		res, err := h.c.Get(h.chat.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), want) {
			t.Errorf("GET %s = %d %s", path, res.StatusCode, res.Header.Get("Content-Type"))
		}
		if !strings.Contains(res.Header.Get("Content-Security-Policy"), "script-src 'self'") {
			t.Errorf("GET %s has no content security policy", path)
		}
	}
	for _, path := range []string{"/assets/nope.js", "/api/nope"} {
		res, err := h.c.Get(h.chat.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != 404 {
			t.Errorf("GET %s = %d, want 404", path, res.StatusCode)
		}
	}
}
