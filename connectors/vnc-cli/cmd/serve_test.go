package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/serve"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

func TestServeSandbox(t *testing.T) {
	standalone(t)
	state, err := os.MkdirTemp("/tmp", "vt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(state) })
	t.Setenv("TREBI_STATE_DIR", state)
	// The sandbox replaces the server and the password.
	t.Setenv(config.EnvServer, "192.0.2.1")
	t.Setenv(config.EnvPassword, "not-sent")

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	r := NewRunner(inR, outW, io.Discard)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- r.App("test").Run(ctx, []string{"vnc-cli", "serve", "--sandbox"})
		outW.Close()
	}()
	notes := make(chan map[string]any, 100)
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				notes <- m
			}
		}
		close(notes)
	}()
	io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol":"`+sdk.Protocol+`"}}`+"\n")
	io.WriteString(inW, `{"jsonrpc":"2.0","method":"initialized","params":{}}`+"\n")

	want := []string{"status:connecting", "status:connected", "event:" + serve.EventChanged, "event:" + serve.EventStable}
	timeout := time.After(10 * time.Second)
	for len(want) > 0 {
		select {
		case m, ok := <-notes:
			if !ok {
				t.Fatalf("serve ended: %v", <-done)
			}
			p, _ := m["params"].(map[string]any)
			var got string
			switch m["method"] {
			case "status":
				got = "status:" + p["state"].(string)
			case "event":
				got = "event:" + p["type"].(string)
			default:
				continue
			}
			if got == want[0] {
				want = want[1:]
			}
		case <-timeout:
			t.Fatalf("no %s in time", want[0])
		}
	}

	// A command of a job reaches serve.
	if res := mustRun(t, "", "--session=auto", "info"); !strings.Contains(res.stdout, "Sandbox") {
		t.Fatalf("info: %q", res.stdout)
	}

	inW.Close()
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
}
