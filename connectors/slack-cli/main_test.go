package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/spec"
)

// runCLI runs the CLI against a fake Slack server and returns stdout.
func runCLI(t *testing.T, h http.HandlerFunc, args ...string) (string, error) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("SLACK_API_URL", srv.URL)

	// A config with one session workspace so no selection flag is needed.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	t.Setenv("SLACK_CLI_CONFIG", path)
	cfg := &config.Config{Workspace: "acme"}
	cfg.Set("acme", config.Workspace{Token: "xoxb-test", TeamID: "T1", Domain: "acme", URL: "https://acme.slack.com/", UserID: "U1"})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load()
	if err != nil {
		t.Fatal(err)
	}
	app := newApp(cfg, s)
	var buf bytes.Buffer
	app.Writer = &buf
	err = app.Run(context.Background(), append([]string{"slack-cli"}, args...))
	return buf.String(), err
}

func TestE2EAPIPassthrough(t *testing.T) {
	out, err := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/auth.test") {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Write([]byte(`{"ok":true,"team":"Acme","user":"bot"}`))
	}, "--json", "api", "auth.test")
	if err != nil {
		t.Fatal(err)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("output not JSON: %q", out)
	}
	if resp["team"] != "Acme" {
		t.Errorf("team = %v", resp["team"])
	}
}

func TestE2ETypedFlags(t *testing.T) {
	// chat.postMessage with typed flags from the spec.
	out, err := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat.postMessage") {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = r.ParseForm()
		if r.PostForm.Get("channel") != "C123" {
			t.Errorf("channel = %q", r.PostForm.Get("channel"))
		}
		if r.PostForm.Get("text") != "hello" {
			t.Errorf("text = %q", r.PostForm.Get("text"))
		}
		w.Write([]byte(`{"ok":true,"ts":"1.2","channel":"C123"}`))
	}, "--json", "--no-resolve", "chat", "post-message", "--channel", "C123", "--text", "hello")
	if err != nil {
		t.Fatal(err)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("output not JSON: %q", out)
	}
	if resp["ts"] != "1.2" {
		t.Errorf("ts = %v", resp["ts"])
	}
}

func TestE2EAPIError(t *testing.T) {
	_, err := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
	}, "api", "conversations.info", "--param", "channel=C0")
	if err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Fatalf("want channel_not_found error, got %v", err)
	}
}

func TestE2EListenCount(t *testing.T) {
	// A bot token cannot open a socket; poll mode needs --channel. One message
	// then --count 1 stops. The baseline is the first poll (emits nothing), so
	// the new message arrives on the second poll.
	var phase int
	out, err := runCLI(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if !strings.HasSuffix(r.URL.Path, "/conversations.history") {
			w.Write([]byte(`{"ok":true}`))
			return
		}
		if phase == 0 {
			phase = 1
			w.Write([]byte(`{"ok":true,"messages":[]}`))
			return
		}
		w.Write([]byte(`{"ok":true,"messages":[{"type":"message","ts":"5.5","user":"U2","text":"ping","channel":"C1"}]}`))
	}, "--no-resolve", "listen", "--channel", "C1", "--poll-interval", "100ms", "--count", "1", "--timeout", "5s")
	if err != nil {
		t.Fatal(err)
	}
	var e map[string]any
	line := strings.TrimSpace(out)
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		t.Fatalf("listen output not JSON line: %q", out)
	}
	if e["text"] != "ping" || e["channel"] != "C1" {
		t.Errorf("event = %v", e)
	}
}
