package agents

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/bespinian/keera-chat/internal/gateway"
)

func serve(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func gatewayError(t *testing.T, err error) *gateway.Error {
	t.Helper()
	e, ok := errors.AsType[*gateway.Error](err)
	if !ok {
		t.Fatalf("err = %v (%T), want *gateway.Error", err, err)
	}
	return e
}

func TestRedirectNotFollowed(t *testing.T) {
	var reached atomic.Bool
	elsewhere := serve(t, func(http.ResponseWriter, *http.Request) { reached.Store(true) })
	base := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere, http.StatusFound)
	})

	_, err := New().Agents(context.Background(), base, "secret-token")
	if e := gatewayError(t, err); e.Message != NoAnswer {
		t.Errorf("message = %q", e.Message)
	}
	if reached.Load() {
		t.Error("followed the redirect, taking the token along")
	}
}

func TestErrors(t *testing.T) {
	t.Run("in the gateway's shape", func(t *testing.T) {
		base := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthenticated","message":"sign in again"}}`))
		})
		_, err := New().Cancel(context.Background(), base, "t", "task_1")
		if !gateway.IsUnauthorized(err) {
			t.Errorf("err = %v, want the sign-in rejected", err)
		}
	})
	t.Run("a proxy's page", func(t *testing.T) {
		base := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`<html>down</html>`))
		})
		_, err := New().Agents(context.Background(), base, "t")
		if e := gatewayError(t, err); e.Status != http.StatusBadGateway || e.Message != NoAnswer {
			t.Errorf("err = %d %q", e.Status, e.Message)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		srv.Close()
		_, err := New().Agents(context.Background(), srv.URL, "t")
		if e := gatewayError(t, err); e.Message != NoAnswer {
			t.Errorf("message = %q", e.Message)
		}
	})
	t.Run("an unreadable answer", func(t *testing.T) {
		base := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`not json`)) })
		if _, err := New().Agents(context.Background(), base, "t"); err == nil {
			t.Error("no error")
		}
	})
}

// Ids from the page cannot reach another path on Keera Agents.
func TestIdsEscaped(t *testing.T) {
	var path string
	base := serve(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"task":{},"events":[]}`))
	})
	if _, _, err := New().Events(context.Background(), base+"/", "t", "a/b?c", 7); err != nil {
		t.Fatal(err)
	}
	if want := "/api/v1/tasks/a%2Fb%3Fc/events"; path != want {
		t.Errorf("path = %s, want %s", path, want)
	}
}

func TestDone(t *testing.T) {
	for state, want := range map[string]bool{
		"queued": false, "running": false, "succeeded": true, "failed": true, "cancelled": true,
	} {
		if (Task{State: state}).Done() != want {
			t.Errorf("%s: Done = %v", state, !want)
		}
	}
}
