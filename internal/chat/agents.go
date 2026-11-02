package chat

import (
	"cmp"
	"net/http"
	"strconv"

	"github.com/bespinian/keera-chat/internal/agents"
	"github.com/bespinian/keera-chat/internal/gateway"
)

// agentsBase is where chat reaches Keera Agents for the caller, or "" when
// the gateway has none connected.
func (s *Server) agentsBase(c *caller) string {
	if c.me.AgentsURL == "" {
		return ""
	}
	return cmp.Or(s.opts.AgentsInternalURL, c.me.AgentsURL)
}

// offeredAgents lists the caller's agents for /api/me. A failure leaves them
// out, so chat with models still works.
func (s *Server) offeredAgents(r *http.Request, c *caller) ([]agents.Agent, error) {
	base := s.agentsBase(c)
	if base == "" {
		return []agents.Agent{}, nil
	}
	list, err := s.agents.Agents(r.Context(), base, c.token)
	if err != nil {
		if gateway.IsUnauthorized(err) {
			return nil, err
		}
		s.log.Warn("reading the agents failed", "error", err)
		return []agents.Agent{}, nil
	}
	if list == nil {
		list = []agents.Agent{}
	}
	return list, nil
}

// agentTask is a task as the page follows it.
type agentTask struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Done   bool   `json:"done"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
	// Steps are the tools the agent used since After, in its own short
	// words, such as "shell: go test ./...".
	Steps []string `json:"steps"`
	// After is the cursor for the next read.
	After int64 `json:"after"`
}

func taskOut(t agents.Task, events []agents.Event, after int64) agentTask {
	out := agentTask{
		ID: t.ID, State: t.State, Done: t.Done(), Result: t.Result, Error: t.Error,
		Steps: []string{}, After: after,
	}
	for _, e := range events {
		if e.Kind == "tool_call" && e.Text != "" {
			out.Steps = append(out.Steps, e.Text)
		}
		out.After = max(out.After, e.ID)
	}
	return out
}

// tellAgent sends a message to an agent and returns its task at once. The
// page then follows it with agentTask, so a reply survives a reload.
func (s *Server) tellAgent(w http.ResponseWriter, r *http.Request, c *caller) {
	base := s.agentsBase(c)
	if base == "" {
		writeError(w, http.StatusNotFound, "no_agents", "Keera Agents is not connected to this Keera Gateway")
		return
	}
	var in struct {
		Conversation string `json:"conversation"`
		Text         string `json:"text"`
	}
	if !s.readJSON(w, r, &in) {
		return
	}
	t, err := s.agents.Send(r.Context(), base, c.token, r.PathValue("id"), in.Conversation, in.Text)
	if err != nil {
		s.gatewayFailed(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, taskOut(t, nil, 0))
}

func (s *Server) agentTask(w http.ResponseWriter, r *http.Request, c *caller) {
	base := s.agentsBase(c)
	if base == "" {
		writeError(w, http.StatusNotFound, "no_agents", "Keera Agents is not connected to this Keera Gateway")
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	t, events, err := s.agents.Events(r.Context(), base, c.token, r.PathValue("id"), after)
	if err != nil {
		s.gatewayFailed(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, taskOut(t, events, after))
}

func (s *Server) cancelAgentTask(w http.ResponseWriter, r *http.Request, c *caller) {
	base := s.agentsBase(c)
	if base == "" {
		writeError(w, http.StatusNotFound, "no_agents", "Keera Agents is not connected to this Keera Gateway")
		return
	}
	t, err := s.agents.Cancel(r.Context(), base, c.token, r.PathValue("id"))
	if err != nil {
		s.gatewayFailed(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, taskOut(t, nil, 0))
}
