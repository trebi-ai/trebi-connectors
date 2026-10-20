package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/config"
)

func newTestClient(t *testing.T, w config.Workspace, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("SLACK_API_URL", srv.URL)
	c := New(w)
	c.MaxRetries = 2
	return c
}

func TestCallSuccess(t *testing.T) {
	c := newTestClient(t, config.Workspace{Token: "xoxb-1"}, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/auth.test") {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer xoxb-1" {
			t.Errorf("auth header = %q", got)
		}
		w.Write([]byte(`{"ok":true,"team":"Acme"}`))
	})
	var out struct {
		Team string `json:"team"`
	}
	if err := c.CallJSON(context.Background(), "auth.test", url.Values{}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Team != "Acme" {
		t.Errorf("team = %q", out.Team)
	}
}

func TestSessionTokenInForm(t *testing.T) {
	c := newTestClient(t, config.Workspace{Token: "xoxc-session", Cookie: "xoxd-1"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("session token should not use Authorization header")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("token") != "xoxc-session" {
			t.Errorf("form token = %q", r.PostForm.Get("token"))
		}
		if r.Header.Get("Origin") != "https://app.slack.com" {
			t.Errorf("missing browser Origin header")
		}
		w.Write([]byte(`{"ok":true}`))
	})
	if _, err := c.Call(context.Background(), "auth.test", url.Values{}); err != nil {
		t.Fatal(err)
	}
}

func TestAPIError(t *testing.T) {
	c := newTestClient(t, config.Workspace{Token: "xoxb-1"}, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":false,"error":"missing_scope","needed":"chat:write","provided":"chat:read"}`))
	})
	_, err := c.Call(context.Background(), "chat.postMessage", url.Values{})
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("want APIError, got %v", err)
	}
	if ae.Code != "missing_scope" || ae.Needed != "chat:write" {
		t.Errorf("APIError = %+v", ae)
	}
}

func TestRetryAfter429(t *testing.T) {
	var hits int32
	c := newTestClient(t, config.Workspace{Token: "xoxb-1"}, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
	if _, err := c.Call(context.Background(), "users.list", url.Values{}); err != nil {
		t.Fatal(err)
	}
	if hits != 2 {
		t.Errorf("hits = %d, want 2", hits)
	}
}

func TestRateLimitExhausted(t *testing.T) {
	c := newTestClient(t, config.Workspace{Token: "xoxb-1"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c.MaxRetries = 1
	_, err := c.Call(context.Background(), "users.list", url.Values{})
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("want RateLimitError, got %v", err)
	}
}

func TestRefreshOnAuthError(t *testing.T) {
	var hits int32
	c := newTestClient(t, config.Workspace{Token: "xoxc-old", Cookie: "xoxd-1"}, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if atomic.AddInt32(&hits, 1) == 1 {
			w.Write([]byte(`{"ok":false,"error":"token_expired"}`))
			return
		}
		if r.PostForm.Get("token") != "xoxc-new" {
			t.Errorf("after refresh token = %q", r.PostForm.Get("token"))
		}
		w.Write([]byte(`{"ok":true}`))
	})
	var refreshed int32
	c.Refresh = func(ctx context.Context) (config.Workspace, error) {
		atomic.AddInt32(&refreshed, 1)
		return config.Workspace{Token: "xoxc-new", Cookie: "xoxd-1"}, nil
	}
	if _, err := c.Call(context.Background(), "auth.test", url.Values{}); err != nil {
		t.Fatal(err)
	}
	if refreshed != 1 || hits != 2 {
		t.Errorf("refreshed = %d, hits = %d", refreshed, hits)
	}
}

func TestBaseURLSessionUsesTeamURL(t *testing.T) {
	t.Setenv("SLACK_API_URL", "")
	w := config.Workspace{Token: "xoxc-1", URL: "https://acme.slack.com/"}
	if got := BaseURL(w); got != "https://acme.slack.com/api/" {
		t.Errorf("session base = %q", got)
	}
	b := config.Workspace{Token: "xoxb-1"}
	if got := BaseURL(b); got != DefaultBaseURL {
		t.Errorf("oauth base = %q", got)
	}
}
