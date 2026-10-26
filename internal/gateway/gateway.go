// Package gateway calls the Keera Gateway control API with the person's
// sign-in.
//
// Chat holds no API key secrets. Messages go through the playground route
// with one of the person's keys, by id. So that key's guardrails, budgets,
// audit log and usage all apply.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ControlPrefix is the control API's path.
const ControlPrefix = "/control"

// ClientName tells chat's requests apart from the panel's playground.
const ClientName = "keera-chat"

// NoAnswer is shown when the gateway is unreachable or its answer unreadable.
const NoAnswer = "Keera Gateway did not answer; try again in a moment"

// TooLarge is shown when a conversation is too large to send.
const TooLarge = "This chat is too large to send. Start a new chat, or attach fewer or smaller images."

// callTimeout bounds every call but Chat, whose reply can take minutes.
const callTimeout = 20 * time.Second

type Client struct {
	base string
	http *http.Client
}

// New returns a client for the gateway at base, such as http://gateway:8080.
func New(base string) *Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 32
	return &Client{base: strings.TrimRight(base, "/"), http: &http.Client{
		Transport: t,
		// A redirect means a proxy's sign-in page. Following it would drop
		// the token and parse HTML as JSON.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Error is a refusal, worded for people.
type Error struct {
	Status  int
	Code    string
	Message string
	// Detail is the original text when Message replaced it.
	Detail string
	// plain means Message is chat's wording, not the gateway's.
	plain bool
}

func (e *Error) Error() string { return e.Message }

// NewError words an error for people. Gateway refusals with a code are
// shown as they are. Others, and codes in reworded, get a plain sentence,
// with the original kept as Detail.
func NewError(status int, code Code, msg string) *Error {
	e := &Error{Status: status, Code: string(code), Message: msg}
	if code == "" || msg == "" || reworded[e.Code] {
		e.reword()
	}
	return e
}

// reword replaces Message with chat's wording and keeps it as Detail.
func (e *Error) reword() {
	e.Message, e.Detail, e.plain = plain(e.Status, e.Code, ""), e.Message, true
}

// Naming adds the model's name to chat's own wording.
func (e *Error) Naming(model string) *Error {
	if e.plain {
		e.Message = plain(e.Status, e.Code, model)
	}
	return e
}

// reworded are gateway codes whose messages are not meant for people.
var reworded = map[string]bool{"upstream_error": true, "upstream_unavailable": true, tooLong: true}

// tooLong is the gateway's code for exceeding the context. Its message is
// meant for agents, which compact on it.
const tooLong = "context_length_exceeded"

// plain says what went wrong in simple words. model may be empty.
func plain(status int, code, model string) string {
	if code == tooLong {
		if model == "" {
			model = "the model"
		}
		return "This conversation is too long for " + model +
			". Start a new chat, or choose a model that can read more."
	}
	if model == "" {
		model = "The model"
	}
	switch status {
	case http.StatusBadRequest:
		return model + " could not answer this message. Try changing it, or start a new chat."
	case http.StatusUnauthorized:
		return model + " is not set up correctly. Choose another model, or ask your administrator."
	case http.StatusForbidden:
		return "You are not allowed to use this. Choose another model, or ask your administrator."
	case http.StatusNotFound:
		return model + " is no longer available. Choose another model."
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return model + " took too long to answer. Try a shorter message, or start a new chat."
	case http.StatusRequestEntityTooLarge:
		return TooLarge
	case http.StatusTooManyRequests:
		return model + " is too busy right now. Wait a moment and try again."
	}
	if status >= 500 {
		return model + " is not working right now. Try again in a little while."
	}
	return "Something went wrong. Try again."
}

// Code is an error code. A numeric code, such as vLLM's, is ignored rather
// than failing the whole error.
type Code string

func (c *Code) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*c = Code(s)
	}
	return nil
}

// unauthenticated is the gateway's code for a rejected sign-in.
const unauthenticated = "unauthenticated"

// IsUnauthorized reports whether the sign-in is no longer valid. Other 401s
// may come from a model server.
func IsUnauthorized(err error) bool {
	e, ok := errors.AsType[*Error](err)
	return ok && e.Status == http.StatusUnauthorized && e.Code == unauthenticated
}

// AuthConfig lists the gateway's sign-in options.
type AuthConfig struct {
	SSO       bool       `json:"sso"`
	Providers []Provider `json:"providers"`
	Passkeys  bool       `json:"passkeys"`
}

type Provider struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

// Token is a sign-in redeemed from a one-time code.
type Token struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Me is the signed-in person.
type Me struct {
	UserID  string       `json:"user_id"`
	Email   string       `json:"email"`
	Name    string       `json:"name"`
	Role    string       `json:"role"`
	OrgID   string       `json:"org_id"`
	OrgName string       `json:"org_name"`
	Orgs    []Membership `json:"orgs"`
}

type Membership struct {
	ID      string `json:"org_id"`
	Name    string `json:"org_name"`
	Invited bool   `json:"invited"`
}

// Access is the person's keys.
type Access struct {
	Keys []Key `json:"keys"`
}

type Key struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Prefix        string   `json:"prefix"`
	ProjectName   string   `json:"project_name"`
	Kind          string   `json:"kind"`
	State         string   `json:"state"`
	AllowedModels []string `json:"allowed_models"`
	Scopes        []Scope  `json:"scopes"`
}

// Scope is one level of limits: organisation, project or key.
type Scope struct {
	Type         string     `json:"type"`
	Name         string     `json:"name"`
	BudgetMicros int64      `json:"budget_micros"`
	SpendMicros  int64      `json:"spend_micros"`
	Period       string     `json:"period"`
	ResetsAt     *time.Time `json:"resets_at"`
}

type Model struct {
	Alias        string `json:"alias"`
	Kind         string `json:"kind"`
	Enabled      bool   `json:"enabled"`
	DisplayName  string `json:"display_name"`
	Description  string `json:"description"`
	Subscription bool   `json:"subscription"`
	// MaxContext is the context size in tokens, or 0 if unknown.
	MaxContext int `json:"max_context_tokens"`
}

type Router struct {
	Alias       string `json:"alias"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

func (c *Client) AuthConfig(ctx context.Context) (AuthConfig, error) {
	var out AuthConfig
	err := c.call(ctx, "GET", "/auth/config", "", nil, &out)
	return out, err
}

// Redeem trades a sign-in's one-time code for a token. The verifier proves
// chat started that sign-in.
func (c *Client) Redeem(ctx context.Context, code, verifier, label string) (Token, error) {
	var out Token
	err := c.call(ctx, "POST", "/auth/cli/token", "", map[string]string{
		"code": code, "verifier": verifier, "label": label,
	}, &out)
	return out, err
}

func (c *Client) Logout(ctx context.Context, token string) error {
	return c.call(ctx, "POST", "/auth/logout", token, nil, nil)
}

func (c *Client) Me(ctx context.Context, token string) (Me, error) {
	var out Me
	err := c.call(ctx, "GET", "/v1/me", token, nil, &out)
	return out, err
}

// SwitchOrg moves the sign-in to another organisation.
func (c *Client) SwitchOrg(ctx context.Context, token, orgID string) error {
	return c.call(ctx, "POST", "/v1/me/org", token, map[string]string{"org_id": orgID}, nil)
}

func (c *Client) Access(ctx context.Context, token string) (Access, error) {
	var out Access
	err := c.call(ctx, "GET", "/v1/access", token, nil, &out)
	return out, err
}

func (c *Client) Models(ctx context.Context, token, orgID string) ([]Model, error) {
	return list[Model](ctx, c, "/v1/models", token, orgID)
}

func (c *Client) Routers(ctx context.Context, token, orgID string) ([]Router, error) {
	return list[Router](ctx, c, "/v1/routers", token, orgID)
}

// list reads an organisation's {"data": [...]}.
func list[T any](ctx context.Context, c *Client, path, token, orgID string) ([]T, error) {
	var out struct {
		Data []T `json:"data"`
	}
	err := c.call(ctx, "GET", path+"?"+url.Values{"org_id": {orgID}}.Encode(), token, nil, &out)
	return out.Data, err
}

// Chat sends a chat completions request with key keyID. The caller streams
// and closes the response. Cancelling ctx stops the model.
func (c *Client) Chat(ctx context.Context, token, keyID string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+ControlPrefix+"/v1/playground/chat?"+url.Values{"key_id": {keyID}}.Encode(),
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	// Stop proxies from gzipping the stream and buffering tokens.
	req.Header.Set("Accept-Encoding", "identity")
	res, err := c.send(req, token)
	// Any other 401 is from the model server. Its message is for admins.
	if e, ok := errors.AsType[*Error](err); ok && e.Status == http.StatusUnauthorized &&
		e.Code != unauthenticated && !e.plain {
		e.reword()
	}
	return res, err
}

func (c *Client) call(ctx context.Context, method, path, token string, in, out any) error {
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
	req, err := http.NewRequestWithContext(ctx, method, c.base+ControlPrefix+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	res, err := c.send(req, token)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(out); err != nil {
		return fmt.Errorf("reading Keera Gateway's answer to %s %s: %w", method, path, err)
	}
	return nil
}

// send makes req with token, if any, and turns a refusal into an *Error.
// The caller closes the response body.
func (c *Client) send(req *http.Request, token string) (*http.Response, error) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// The gateway falls back to User-Agent without X-Keera-Client.
	req.Header.Set("X-Keera-Client", ClientName)
	req.Header.Set("User-Agent", ClientName)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, unreachable(err)
	}
	if res.StatusCode/100 != 2 {
		defer func() { _ = res.Body.Close() }()
		return nil, readError(res)
	}
	return res, nil
}

// readError reads {"error": {"message", "code"}}. Anything else, such as a
// proxy's HTML page, keeps only the status.
func readError(res *http.Response) error {
	var env struct {
		Error struct {
			Message string `json:"message"`
			Code    Code   `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&env)
	return NewError(res.StatusCode, env.Error.Code, env.Error.Message)
}

// unreachable keeps cancellation as is and turns other failures into NoAnswer.
func unreachable(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return &Error{Status: http.StatusBadGateway, Message: NoAnswer}
}
