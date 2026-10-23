// Package gateway is Keera Gateway as Keera Chat sees it: the few control API
// routes a chat needs, called with the person's own sign-in.
//
// Chat holds no API key secrets. A person's messages go through the gateway's
// playground route, which sends them with one of the person's keys named by
// id. So they meet that key's guardrails, budgets and filters, are logged in
// the audit log, and show in the person's usage, like any other request.
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

// ControlPrefix is where the gateway serves its control API.
const ControlPrefix = "/control"

// ClientName is how chat names itself to the gateway, so its requests are
// told apart from the panel's playground in usage and on the map.
const ClientName = "keera-chat"

// NoAnswer is what a person is told when the gateway cannot be reached, or
// gave an answer chat cannot read.
const NoAnswer = "Keera Gateway did not answer; try again in a moment"

// callTimeout bounds every call but the chat itself, whose reply can take
// minutes.
const callTimeout = 20 * time.Second

type Client struct {
	base string
	http *http.Client
}

// New returns a client for the gateway at base, an origin such as
// http://gateway:8080.
func New(base string) *Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 32
	return &Client{base: strings.TrimRight(base, "/"), http: &http.Client{
		Transport: t,
		// A redirect from the control API means a proxy in front of it is
		// sending us to a sign-in page. Following it would lose the bearer
		// token and parse HTML as JSON.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Error is a refusal, worded for a person.
type Error struct {
	Status int
	Code   string
	// Message is what the person is told.
	Message string
	// Detail is the text Message stands in for, when it was not shown: a
	// model server's words, passed on by the gateway, or the gateway's own
	// when they were not meant for a person.
	Detail string
	// plain is set when Message is chat's own wording, not the gateway's.
	plain bool
}

func (e *Error) Error() string { return e.Message }

// NewError words an error for a person. The gateway's own refusals carry a
// code and are written for people, so they are shown as they are. Anything
// else, such as a model server's error, and the few codes that are about the
// gateway's insides or worded for agents, get a plain sentence instead, with
// the text kept as the detail.
func NewError(status int, code Code, msg string) *Error {
	e := &Error{Status: status, Code: string(code), Message: msg}
	if code == "" || msg == "" || reworded[e.Code] {
		e.Message, e.Detail, e.plain = plain(status, e.Code, ""), msg, true
	}
	return e
}

// Naming puts the model's name in chat's own wording, so the person knows
// which model failed. The gateway's own words are left as they are.
func (e *Error) Naming(model string) *Error {
	if e.plain {
		e.Message = plain(e.Status, e.Code, model)
	}
	return e
}

// reworded are the gateway's own codes whose words are not for a person:
// those about its insides, and tooLong.
var reworded = map[string]bool{"upstream_error": true, "upstream_unavailable": true, tooLong: true}

// tooLong is the code the gateway gives a conversation that does not fit in
// the model's context. Its words are for agents, which compact a conversation
// when they read them, so a person gets chat's own.
const tooLong = "context_length_exceeded"

// plain says in simple words what went wrong, for an error with no words of
// its own a person could follow. model is the model's name, if known.
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
		return "This chat is too large to send. Start a new chat, or attach fewer or smaller images."
	case http.StatusTooManyRequests:
		return model + " is too busy right now. Wait a moment and try again."
	}
	if status >= 500 {
		return model + " is not working right now. Try again in a little while."
	}
	return "Something went wrong. Try again."
}

// Code is an error's code. The gateway's are text. A model server's error it
// passes on may have a number, such as vLLM's, which is left out rather than
// failing the whole error.
type Code string

func (c *Code) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*c = Code(s)
	}
	return nil
}

// unauthenticated is the code the gateway gives a sign-in it does not accept.
const unauthenticated = "unauthenticated"

// IsUnauthorized reports whether the gateway no longer accepts the sign-in:
// it expired, was ended, or the person was turned off. Another 401 is not
// about the sign-in: the chat route passes on a model server's as it came.
func IsUnauthorized(err error) bool {
	e, ok := errors.AsType[*Error](err)
	return ok && e.Status == http.StatusUnauthorized && e.Code == unauthenticated
}

// ---------------------------------------------------------------- types

// AuthConfig is how the gateway offers signing in.
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

// Me is who a sign-in belongs to.
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

// Access is the person's own keys, with what each may call.
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

// Scope is one level of limits above a key: the organisation, the project
// or the key itself.
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
	// MaxContext is how many tokens the model reads at most, or 0 when the
	// organisation did not say.
	MaxContext int `json:"max_context_tokens"`
}

type Router struct {
	Alias       string `json:"alias"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

// ---------------------------------------------------------------- calls

func (c *Client) AuthConfig(ctx context.Context) (AuthConfig, error) {
	var out AuthConfig
	err := c.call(ctx, "GET", "/auth/config", "", nil, &out)
	return out, err
}

// Redeem trades the one-time code a sign-in came back with for a token. The
// verifier proves this service started that sign-in.
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

// SwitchOrg moves the sign-in to another organisation the person is in.
func (c *Client) SwitchOrg(ctx context.Context, token, orgID string) error {
	return c.call(ctx, "POST", "/v1/me/org", token, map[string]string{"org_id": orgID}, nil)
}

func (c *Client) Access(ctx context.Context, token string) (Access, error) {
	var out Access
	err := c.call(ctx, "GET", "/v1/access", token, nil, &out)
	return out, err
}

func (c *Client) Models(ctx context.Context, token, orgID string) ([]Model, error) {
	var out struct {
		Data []Model `json:"data"`
	}
	err := c.call(ctx, "GET", "/v1/models?"+url.Values{"org_id": {orgID}}.Encode(), token, nil, &out)
	return out.Data, err
}

func (c *Client) Routers(ctx context.Context, token, orgID string) ([]Router, error) {
	var out struct {
		Data []Router `json:"data"`
	}
	err := c.call(ctx, "GET", "/v1/routers?"+url.Values{"org_id": {orgID}}.Encode(), token, nil, &out)
	return out.Data, err
}

// Chat sends one OpenAI chat completions request with the person's key
// keyID, and returns the response for the caller to stream and close. ctx
// bounds the whole reply, so cancelling it stops the model.
func (c *Client) Chat(ctx context.Context, token, keyID string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST",
		c.base+ControlPrefix+"/v1/playground/chat?"+url.Values{"key_id": {keyID}}.Encode(),
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	// Set explicitly, so nothing between here and the model gzips the
	// stream and holds tokens back in a buffer.
	req.Header.Set("Accept-Encoding", "identity")
	res, err := c.send(req, token)
	// A 401 that is not about the sign-in is the model server's, such as for
	// the credentials the gateway holds for it. Its words are for whoever
	// runs Keera.
	if e, ok := errors.AsType[*Error](err); ok && e.Status == http.StatusUnauthorized &&
		e.Code != unauthenticated && !e.plain {
		e.Message, e.Detail, e.plain = plain(e.Status, e.Code, ""), e.Message, true
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

// send makes req with the person's token, if there is one, and turns a
// refusal into an *Error. The caller closes the body of the response.
func (c *Client) send(req *http.Request, token string) (*http.Response, error) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// The gateway reads X-Keera-Client first, and User-Agent where that is
	// not set.
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

// readError reads the gateway's error envelope, {"error": {"message", ...}}.
// Anything else, such as a proxy's HTML page, keeps only the status.
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

// unreachable keeps a cancelled call recognisable as one, and words every
// other failure, a timeout included, for a person.
func unreachable(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return &Error{Status: http.StatusBadGateway, Message: NoAnswer}
}
