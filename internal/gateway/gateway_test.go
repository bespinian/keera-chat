package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// answer serves one fixed answer to every request.
func answer(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func gatewayError(t *testing.T, err error) *Error {
	t.Helper()
	e, ok := errors.AsType[*Error](err)
	if !ok {
		t.Fatalf("err = %v (%T), want *Error", err, err)
	}
	return e
}

func TestRedirectNotFollowed(t *testing.T) {
	var reached atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached.Store(true)
	}))
	t.Cleanup(elsewhere.Close)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	t.Cleanup(proxy.Close)

	_, err := New(proxy.URL).Me(context.Background(), "secret-token")
	if e := gatewayError(t, err); e.Status != http.StatusFound {
		t.Errorf("status = %d, want the redirect's", e.Status)
	}
	if reached.Load() {
		t.Error("followed the redirect, taking the token along")
	}
}

func TestUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	c := New(srv.URL)
	srv.Close()

	e := gatewayError(t, c.Logout(context.Background(), "t"))
	if e.Status != http.StatusBadGateway || e.Message != NoAnswer {
		t.Errorf("err = %d %q, want 502 %q", e.Status, e.Message, NoAnswer)
	}

	// Stopping stays a cancellation, not a gateway failure.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Logout(ctx, "t"); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled err = %v", err)
	}
}

func TestRefusals(t *testing.T) {
	tests := []struct {
		name, body  string
		status      int
		code, msg   string
		detail      string
		unauthorize bool
	}{{
		name:   "kept as the gateway words it",
		status: 402, body: `{"error":{"code":"budget_exceeded","message":"Your budget is spent."}}`,
		code: "budget_exceeded", msg: "Your budget is spent.",
	}, {
		name:   "reworded for people",
		status: 502, body: `{"error":{"code":"upstream_error","message":"dial tcp: refused"}}`,
		code: "upstream_error", msg: "The model is not working", detail: "dial tcp: refused",
	}, {
		name:   "a proxy's page keeps only the status",
		status: 503, body: `<html>down</html>`,
		msg: "The model is not working",
	}, {
		name:   "a numeric code is ignored",
		status: 400, body: `{"error":{"code":400,"message":"bad template"}}`,
		msg: "The model could not answer", detail: "bad template",
	}, {
		name:   "a rejected sign-in",
		status: 401, body: `{"error":{"code":"unauthenticated","message":"sign in again"}}`,
		code: "unauthenticated", msg: "sign in again", unauthorize: true,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := answer(t, tt.status, tt.body).Access(context.Background(), "t")
			e := gatewayError(t, err)
			if e.Status != tt.status || e.Code != tt.code || !strings.HasPrefix(e.Message, tt.msg) ||
				e.Detail != tt.detail {
				t.Errorf("err = %d %q %q (detail %q), want %d %q %q… (detail %q)",
					e.Status, e.Code, e.Message, e.Detail, tt.status, tt.code, tt.msg, tt.detail)
			}
			if IsUnauthorized(err) != tt.unauthorize {
				t.Errorf("IsUnauthorized = %v", !tt.unauthorize)
			}
		})
	}
}

// Only the gateway's own 401 ends the sign-in. A model server's 401 is a
// setup problem, worded for people.
func TestModelServer401(t *testing.T) {
	c := answer(t, 401, `{"error":{"code":"invalid_api_key","message":"Incorrect API key sk-..."}}`)
	_, err := c.Chat(context.Background(), "t", "key", []byte(`{}`))
	if IsUnauthorized(err) {
		t.Fatal("a model server's 401 ended the sign-in")
	}
	e := gatewayError(t, err).Naming("Keera Speed")
	if e.Message != "Keera Speed is not set up correctly. Choose another model, or ask your administrator." ||
		e.Detail != "Incorrect API key sk-..." {
		t.Errorf("err = %q (detail %q)", e.Message, e.Detail)
	}
}

func TestTooLongNamesTheModel(t *testing.T) {
	e := NewError(400, tooLong, "compact now").Naming("Keera Think")
	if !strings.HasPrefix(e.Message, "This conversation is too long for Keera Think.") {
		t.Errorf("message = %q", e.Message)
	}
}

func TestChatSendsKeyAndClient(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	t.Cleanup(srv.Close)

	res, err := New(srv.URL+"/").Chat(context.Background(), "tok", "key/1", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if got.URL.Path != "/control/v1/playground/chat" || got.URL.Query().Get("key_id") != "key/1" {
		t.Errorf("request = %s", got.URL)
	}
	if got.Header.Get("Authorization") != "Bearer tok" || got.Header.Get("X-Keera-Client") != ClientName {
		t.Errorf("headers = %v", got.Header)
	}
}
