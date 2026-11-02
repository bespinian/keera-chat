// Package agents calls Keera Agents with the person's gateway sign-in.
//
// When Keera Gateway has Keera Agents connected, chat offers the person's
// agents next to the models. A message to an agent becomes a task. The agent
// keeps the chat's history itself, so chat sends only the new message, then
// reads the task's steps and answer until it is done.
package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bespinian/keera-chat/internal/gateway"
)

// NoAnswer is shown when Keera Agents is unreachable or its answer
// unreadable.
const NoAnswer = "Keera Agents did not answer; try again in a moment"

const callTimeout = 20 * time.Second

type Client struct {
	http *http.Client
}

func New() *Client {
	return &Client{http: &http.Client{
		// A redirect means a proxy's sign-in page. Following it would drop
		// the token and parse HTML as JSON.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Agent is one the person may give work to.
type Agent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Icon        string `json:"icon"`
	Description string `json:"description"`
	State       string `json:"state"`
}

// Task is one message to an agent.
type Task struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Result string `json:"result"`
	Error  string `json:"error"`
}

// Done reports whether the task has finished, well or not.
func (t Task) Done() bool {
	return t.State == "succeeded" || t.State == "failed" || t.State == "cancelled"
}

// Event is one thing the agent did, such as a tool call.
type Event struct {
	ID   int64  `json:"id"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Agents lists the agents the person may give work to.
func (c *Client) Agents(ctx context.Context, base, token string) ([]Agent, error) {
	var out struct {
		Data []Agent `json:"data"`
	}
	err := c.call(ctx, http.MethodGet, base, "/api/v1/chat/agents", token, nil, &out)
	return out.Data, err
}

// Send gives an agent a message in a conversation. conversation is chat's
// id for it; Keera Agents keeps one history per person and id.
func (c *Client) Send(ctx context.Context, base, token, agentID, conversation, text string) (Task, error) {
	var out Task
	err := c.call(ctx, http.MethodPost, base, "/api/v1/chat/agents/"+url.PathEscape(agentID)+"/messages", token,
		map[string]string{"conversation": conversation, "text": text}, &out)
	return out, err
}

// Events returns the task and what the agent did after event after.
func (c *Client) Events(ctx context.Context, base, token, taskID string, after int64) (Task, []Event, error) {
	var out struct {
		Task   Task    `json:"task"`
		Events []Event `json:"events"`
	}
	err := c.call(ctx, http.MethodGet, base, "/api/v1/tasks/"+url.PathEscape(taskID)+"/events?after="+
		strconv.FormatInt(after, 10), token, nil, &out)
	return out.Task, out.Events, err
}

// Cancel stops a task. The agent stops at its next step.
func (c *Client) Cancel(ctx context.Context, base, token, taskID string) (Task, error) {
	var out Task
	err := c.call(ctx, http.MethodPost, base, "/api/v1/tasks/"+url.PathEscape(taskID)+"/cancel", token, nil, &out)
	return out, err
}

func (c *Client) call(ctx context.Context, method, base, path, token string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", gateway.ClientName)

	res, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return &gateway.Error{Status: http.StatusBadGateway, Message: NoAnswer}
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		return readError(res)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(out); err != nil {
		return fmt.Errorf("reading Keera Agents' answer to %s %s: %w", method, path, err)
	}
	return nil
}

// readError reads Keera Agents' {"error": {"code", "message"}}, the
// gateway's shape. Anything else, such as a proxy's page, keeps the status.
func readError(res *http.Response) error {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&env)
	if env.Error.Message == "" {
		return &gateway.Error{Status: http.StatusBadGateway, Message: NoAnswer}
	}
	return &gateway.Error{Status: res.StatusCode, Code: env.Error.Code, Message: env.Error.Message}
}
