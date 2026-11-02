package chat

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAgents is Keera Agents with one agent, which answers every message
// after one step.
type fakeAgents struct {
	mu        sync.Mutex
	tokens    []string
	sent      map[string]string
	cancelled bool
}

func (f *fakeAgents) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, r.Header.Get("Authorization"))
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer keera_cli_") {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "unauthenticated", "message": "sign in"}})
		return
	}
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/v1/chat/agents":
		reply(map[string]any{"data": []map[string]string{{"id": "agt_scout", "name": "Scout", "state": "running"}}})
	case r.Method == "POST" && r.URL.Path == "/api/v1/chat/agents/agt_scout/messages":
		_ = json.NewDecoder(r.Body).Decode(&f.sent)
		w.WriteHeader(http.StatusCreated)
		reply(map[string]string{"id": "task_1", "state": "queued"})
	case r.Method == "POST" && r.URL.Path == "/api/v1/chat/agents/agt_gone/messages":
		w.WriteHeader(http.StatusNotFound)
		reply(map[string]any{"error": map[string]string{"code": "not_found", "message": "there is no such agent"}})
	case r.Method == "GET" && r.URL.Path == "/api/v1/tasks/task_1/events":
		if r.URL.Query().Get("after") == "0" {
			reply(map[string]any{
				"task": map[string]string{"id": "task_1", "state": "running"},
				"events": []map[string]any{
					{"id": 3, "kind": "tool_call", "text": "shell: go test ./..."},
					{"id": 4, "kind": "tool_result", "text": "ok"},
				},
			})
			return
		}
		reply(map[string]any{
			"task":   map[string]string{"id": "task_1", "state": "succeeded", "result": "All tests pass."},
			"events": []map[string]any{{"id": 5, "kind": "message", "text": "All tests pass."}},
		})
	case r.Method == "POST" && r.URL.Path == "/api/v1/tasks/task_1/cancel":
		f.cancelled = true
		reply(map[string]string{"id": "task_1", "state": "cancelled", "error": "cancelled by ada@example.ch"})
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.String(), http.StatusTeapot)
	}
}

func (h *harness) post(path string, in, out any) int {
	h.t.Helper()
	b, _ := json.Marshal(in)
	req, _ := http.NewRequest(http.MethodPost, h.chat.URL+path, bytes.NewReader(b))
	req.Header.Set("X-Keera-Chat", "1")
	req.Header.Set("Content-Type", "application/json")
	res, err := h.c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	_ = json.NewDecoder(res.Body).Decode(out)
	return res.StatusCode
}

func TestAgents(t *testing.T) {
	h := newHarness(t)
	fake := &fakeAgents{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	h.gw.ConnectAgents(srv.URL)
	h.signIn()

	var me struct {
		Agents    []struct{ ID, Name string }
		AgentsURL string `json:"agents_url"`
	}
	if got := h.do("GET", "/api/me", &me); got != 200 || len(me.Agents) != 1 || me.Agents[0].Name != "Scout" ||
		me.AgentsURL != srv.URL {
		t.Fatalf("GET /api/me = %d, agents %+v at %q", got, me.Agents, me.AgentsURL)
	}

	var task agentTask
	if got := h.post("/api/agents/agt_scout/messages",
		map[string]string{"conversation": "c0123", "text": "Run the tests"}, &task); got != 200 || task.ID != "task_1" ||
		task.Done {
		t.Fatalf("send = %d %+v", got, task)
	}
	fake.mu.Lock()
	sent, token := fake.sent, fake.tokens[len(fake.tokens)-1]
	fake.mu.Unlock()
	// Keera Agents gets the person's own sign-in, and only the new message.
	if sent["conversation"] != "c0123" || sent["text"] != "Run the tests" || !strings.HasPrefix(token, "Bearer keera_cli_") {
		t.Fatalf("Keera Agents got %v with %q", sent, token)
	}

	if got := h.do("GET", "/api/agents/tasks/task_1?after=0", &task); got != 200 || task.Done ||
		len(task.Steps) != 1 || task.Steps[0] != "shell: go test ./..." || task.After != 4 {
		t.Fatalf("first read = %d %+v", got, task)
	}
	if got := h.do("GET", "/api/agents/tasks/task_1?after=4", &task); got != 200 || !task.Done ||
		task.Result != "All tests pass." || len(task.Steps) != 0 || task.After != 5 {
		t.Fatalf("second read = %d %+v", got, task)
	}
	if got := h.post("/api/agents/tasks/task_1/cancel", nil, &task); got != 200 || task.State != "cancelled" {
		t.Fatalf("cancel = %d %+v", got, task)
	}

	// Keera Agents' refusal is passed on in its words.
	var refused struct {
		Error struct{ Code, Message string }
	}
	if got := h.post("/api/agents/agt_gone/messages", map[string]string{"conversation": "c1", "text": "hi"},
		&refused); got != 404 || refused.Error.Message != "there is no such agent" {
		t.Fatalf("an unknown agent = %d %+v", got, refused)
	}
}

func TestNoAgentsConnected(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	var me struct {
		Agents []any
	}
	if got := h.do("GET", "/api/me", &me); got != 200 || me.Agents == nil || len(me.Agents) != 0 {
		t.Fatalf("GET /api/me = %d, agents %v; want an empty list", got, me.Agents)
	}
	var refused struct {
		Error struct{ Code string }
	}
	if got := h.post("/api/agents/agt_scout/messages", map[string]string{"conversation": "c1", "text": "hi"},
		&refused); got != 404 || refused.Error.Code != "no_agents" {
		t.Fatalf("send without Keera Agents = %d %+v", got, refused)
	}
}

// An unreachable Keera Agents leaves the models usable.
func TestAgentsUnreachable(t *testing.T) {
	h := newHarness(t)
	h.gw.ConnectAgents("http://127.0.0.1:1")
	h.signIn()
	var me struct {
		Agents []any
		Models []any
	}
	if got := h.do("GET", "/api/me", &me); got != 200 || len(me.Agents) != 0 || len(me.Models) == 0 {
		t.Fatalf("GET /api/me = %d, agents %v, models %v", got, me.Agents, me.Models)
	}
}
