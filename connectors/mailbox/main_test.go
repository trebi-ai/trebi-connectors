package main

import (
	"bytes"
	"strings"
	"testing"
)

func run(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	app := newApp()
	var out bytes.Buffer
	app.Reader, app.Writer, app.ErrWriter = strings.NewReader(stdin), &out, &out
	err := app.Run(append([]string{"mailbox"}, args...))
	return out.String(), err
}

func TestOpMissingInputInTrebi(t *testing.T) {
	t.Setenv("TREBI_STATE_DIR", t.TempDir())
	t.Setenv("MAILBOX_ADDRESS", "")
	t.Setenv("MAILBOX_PASSWORD", "")
	_, err := run(t, `{"query":"x"}`, "--provider", "gmail", "op", "search")
	if err == nil || !strings.Contains(err.Error(), "Email address (MAILBOX_ADDRESS) is missing: set it in the connection settings") {
		t.Fatalf("got %v", err)
	}
}

func TestOpUnknownField(t *testing.T) {
	t.Setenv("TREBI_STATE_DIR", "")
	t.Setenv("MAILBOX_ADDRESS", "me@example.com")
	t.Setenv("MAILBOX_PASSWORD", "pw")
	_, err := run(t, `{"query":"x","bogus":1}`, "--provider", "gmail", "op", "search")
	if err == nil || !strings.Contains(err.Error(), `unknown field "bogus"`) {
		t.Fatalf("got %v", err)
	}
}

func TestUnknownProvider(t *testing.T) {
	if _, err := run(t, "", "--provider", "aol", "op", "search"); err == nil {
		t.Fatal("want an error")
	}
}
