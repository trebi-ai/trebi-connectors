package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestContractFixtures decodes each fixture strictly into its Go type and
// checks that the Go type writes the same JSON back.
func TestContractFixtures(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("contract/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	types := fixtureTypes()
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var m struct {
				JSONRPC string          `json:"jsonrpc"`
				Params  json.RawMessage `json:"params"`
				Result  json.RawMessage `json:"result"`
				Error   json.RawMessage `json:"error"`
			}
			if err := json.Unmarshal(data, &m); err != nil || m.JSONRPC != "2.0" {
				t.Fatalf("envelope: %v", err)
			}
			if code, ok := strings.CutPrefix(name, "error."); ok {
				var e Error
				if err := json.Unmarshal(m.Error, &e); err != nil {
					t.Fatal(err)
				}
				if e.Code != code {
					t.Fatalf("code %q, want %q", e.Code, code)
				}
				back, _ := json.Marshal(&e)
				if !sameJSON(t, m.Error, back) {
					t.Fatalf("round trip\n got %s\nwant %s", back, m.Error)
				}
				return
			}
			newT, ok := types[name]
			if !ok {
				t.Fatalf("fixture %s has no Go type in fixtureTypes", name)
			}
			body := m.Params
			if len(m.Result) > 0 {
				body = m.Result
			}
			v := newT()
			dec := json.NewDecoder(bytes.NewReader(body))
			dec.DisallowUnknownFields()
			if err := dec.Decode(v); err != nil {
				t.Fatalf("strict decode: %v", err)
			}
			back, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if !sameJSON(t, body, back) {
				t.Fatalf("round trip\n got %s\nwant %s", back, body)
			}
		})
	}
}

// TestVendoredContract fails when a vendored fixture differs from the
// source of truth in a sibling trebi checkout.
func TestVendoredContract(t *testing.T) {
	t.Parallel()
	src := filepath.Join("..", "..", "trebi", "internal", "connectors", "protocol", "testdata", "contract")
	want, err := os.ReadDir(src)
	if err != nil {
		t.Skipf("no sibling trebi checkout: %v", err)
	}
	got, err := os.ReadDir("contract")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("contract/ has %d files, trebi has %d: copy the folder again", len(got), len(want))
	}
	for _, e := range want {
		a, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join("contract", e.Name()))
		if err != nil {
			t.Fatalf("%s is missing: copy it from trebi", e.Name())
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("%s differs from trebi: copy it again", e.Name())
		}
	}
}

func TestAsError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want string
	}{
		{RateLimited("slow", time.Second), CodeRateLimited},
		{fmt.Errorf("wrap: %w", NotFound("x")), CodeNotFound},
		{&net.OpError{Op: "dial", Err: errors.New("refused")}, CodeTransient},
		{fmt.Errorf("call: %w", context.DeadlineExceeded), CodeTransient},
		{errors.New("boom"), CodePermanent},
	}
	for _, c := range cases {
		if got := AsError(c.err).Code; got != c.want {
			t.Errorf("%v → %s, want %s", c.err, got, c.want)
		}
	}
	for rpc, want := range map[int]string{-32601: CodeUnsupported, -32602: CodeInvalid, -32000: CodePermanent} {
		var e Error
		if err := json.Unmarshal([]byte(fmt.Sprintf(`{"code":%d,"message":"x"}`, rpc)), &e); err != nil || e.Code != want {
			t.Errorf("%d → %s (%v), want %s", rpc, e.Code, err, want)
		}
	}
}

func TestSentStorePersistsAndBounds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := newSentStore(dir, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 7 {
		if err := s.put(fmt.Sprintf("k%d", i), SendResult{MessageID: fmt.Sprintf("m%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	re, err := newSentStore(dir, 3)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := re.get("k6"); !ok || r.MessageID != "m6" {
		t.Fatalf("k6 after reload: %v %v", r, ok)
	}
	if _, ok := re.get("k3"); ok {
		t.Fatal("k3 is over the bound and must be gone")
	}
	data, err := os.ReadFile(filepath.Join(dir, SentFile))
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(data, []byte("\n")); n >= 6 {
		t.Fatalf("file has %d lines; compaction must keep it under twice the bound", n)
	}
}

func TestDeriveFeatures(t *testing.T) {
	t.Parallel()
	a := NewSandbox(SandboxConfig{})
	if got := deriveFeatures(a, nil); len(got) != 10 || got[0] != FeatureRoomsList {
		t.Fatalf("derived %v", got)
	}
	got := deriveFeatures(struct{ Adapter }{a}, []string{FeatureTyping, FeatureAttachmentsIn, "bogus"})
	if strings.Join(got, ",") != FeatureAttachmentsIn {
		t.Fatalf("a bare adapter keeps only attachment features, got %v", got)
	}
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return bytes.Equal(xb, yb)
}

// fixtureTypes maps each contract fixture to the Go type of its params or
// result. Every file in contract/ must be listed.
func fixtureTypes() map[string]func() any {
	return map[string]func() any{
		"initialize.request":       func() any { return &InitializeParams{} },
		"initialize.result":        func() any { return &InitializeResult{} },
		"initialized":              func() any { return &Empty{} },
		"ping.request":             func() any { return &Empty{} },
		"ping.result":              func() any { return &Empty{} },
		"shutdown.request":         func() any { return &Empty{} },
		"shutdown.result":          func() any { return &Empty{} },
		"empty.result":             func() any { return &Empty{} },
		"event.message":            func() any { return &Event{} },
		"event.message.thread":     func() any { return &Event{} },
		"event.reaction":           func() any { return &Event{} },
		"status.connected":         func() any { return &Status{} },
		"status.auth_required":     func() any { return &Status{} },
		"auth.status.request":      func() any { return &Empty{} },
		"auth.status.result":       func() any { return &AuthStatusResult{} },
		"auth.begin.request":       func() any { return &AuthBeginParams{} },
		"auth.begin.result":        func() any { return &AuthBeginResult{} },
		"auth.submit.request":      func() any { return &AuthSubmitParams{} },
		"auth.submit.result":       func() any { return &AuthSubmitResult{} },
		"auth.cancel.request":      func() any { return &AuthFlowParams{} },
		"auth.logout.request":      func() any { return &Empty{} },
		"auth.step.qr":             func() any { return &AuthStepParams{} },
		"auth.step.device_code":    func() any { return &AuthStepParams{} },
		"auth.step.url":            func() any { return &AuthStepParams{} },
		"auth.step.input":          func() any { return &AuthStepParams{} },
		"auth.step.wait":           func() any { return &AuthStepParams{} },
		"auth.done":                func() any { return &AuthDoneParams{} },
		"auth.done.failed":         func() any { return &AuthDoneParams{} },
		"messages.send.request":    func() any { return &SendParams{} },
		"messages.send.result":     func() any { return &SendResult{} },
		"messages.edit.request":    func() any { return &EditParams{} },
		"messages.history.request": func() any { return &HistoryQuery{} },
		"messages.history.result":  func() any { return &EventPage{} },
		"messages.seen.request":    func() any { return &SeenParams{} },
		"events.replay.request":    func() any { return &ReplayParams{} },
		"events.replay.result":     func() any { return &ReplayResult{} },
		"rooms.list.request":       func() any { return &RoomQuery{} },
		"rooms.list.result":        func() any { return &RoomPage{} },
		"rooms.get.request":        func() any { return &RoomParams{} },
		"rooms.get.result":         func() any { return &RoomResult{} },
		"rooms.open.request":       func() any { return &RoomOpenParams{} },
		"rooms.open.result":        func() any { return &RoomResult{} },
		"threads.list.request":     func() any { return &ThreadQuery{} },
		"threads.list.result":      func() any { return &ThreadPage{} },
		"threads.create.request":   func() any { return &CreateThreadParams{} },
		"threads.create.result":    func() any { return &ThreadResult{} },
		"typing.request":           func() any { return &TypingParams{} },
		"reactions.add.request":    func() any { return &ReactionParams{} },
	}
}
