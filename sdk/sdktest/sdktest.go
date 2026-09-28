// Package sdktest runs an adapter in-process from the daemon side. Play
// runs a contract transcript against it; Conn sends single requests. Use it
// in the unit tests of an adapter.
package sdktest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// AnyValue in a transcript matches any value.
const AnyValue = "<any>"

// Wait is how long Conn waits for one line from the adapter.
const Wait = 5 * time.Second

// Line is one transcript line. From is "daemon" or "adapter".
type Line struct {
	From string          `json:"from"`
	Msg  json.RawMessage `json:"msg"`
}

// Message is one JSON-RPC 2.0 object from the adapter.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *sdk.Error      `json:"error,omitempty"`
}

// Conn is the daemon end of one in-process session.
type Conn struct {
	in     *io.PipeWriter
	lines  chan []byte
	served chan error
	cancel context.CancelFunc

	mu     sync.Mutex
	nextID int
	notes  []Message
}

// Start runs sdk.Serve for a on in-memory pipes. Close ends it.
func Start(a sdk.Adapter, opts ...sdk.Option) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &Conn{in: inW, lines: make(chan []byte, 64), served: make(chan error, 1), cancel: cancel, nextID: 1000}
	go func() { // ends when Serve returns
		err := sdk.Serve(ctx, a, append(opts, sdk.WithIO(inR, outW))...)
		outW.Close() //nolint:errcheck // a pipe close does not fail
		c.served <- err
	}()
	go func() { // ends when the adapter output closes
		defer close(c.lines)
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 0, 64*1024), sdk.MaxLine+1)
		for sc.Scan() {
			c.lines <- bytes.Clone(sc.Bytes())
		}
	}()
	return c
}

// Write sends one raw line to the adapter.
func (c *Conn) Write(line []byte) error {
	_, err := c.in.Write(append(bytes.TrimRight(line, "\n"), '\n'))
	return err
}

// Read returns the next line of the adapter.
func (c *Conn) Read() (Message, []byte, error) {
	select {
	case line, ok := <-c.lines:
		if !ok {
			return Message{}, nil, io.EOF
		}
		var m Message
		if err := json.Unmarshal(line, &m); err != nil {
			return Message{}, line, fmt.Errorf("adapter wrote %q: %w", line, err)
		}
		return m, line, nil
	case <-time.After(Wait):
		return Message{}, nil, errors.New("no line from the adapter in time")
	}
}

// Notify sends one notification.
func (c *Conn) Notify(method string, params any) error {
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": orEmpty(params)})
	if err != nil {
		return err
	}
	return c.Write(b)
}

// Call sends one request and reads until its answer. It keeps the
// notifications it reads for Notes. An error answer returns as *sdk.Error.
func (c *Conn) Call(method string, params, result any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": orEmpty(params)})
	if err != nil {
		return err
	}
	if err := c.Write(b); err != nil {
		return err
	}
	for {
		m, _, err := c.Read()
		if err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		if m.Method != "" {
			c.mu.Lock()
			c.notes = append(c.notes, m)
			c.mu.Unlock()
			continue
		}
		if string(m.ID) != strconv.Itoa(id) {
			return fmt.Errorf("%s: answer id %s, want %d", method, m.ID, id)
		}
		if m.Error != nil {
			return m.Error
		}
		if result != nil {
			return json.Unmarshal(m.Result, result)
		}
		return nil
	}
}

// Initialize runs initialize and initialized.
func (c *Conn) Initialize(p sdk.InitializeParams) (sdk.InitializeResult, error) {
	if p.Protocol == "" {
		p.Protocol = sdk.Protocol
	}
	var res sdk.InitializeResult
	if err := c.Call(sdk.MethodInitialize, p, &res); err != nil {
		return res, err
	}
	return res, c.Notify(sdk.MethodInitialized, sdk.Empty{})
}

// WaitNote returns the first kept or new notification of method, and
// drops it from the kept list.
func (c *Conn) WaitNote(method string) (Message, error) {
	for {
		c.mu.Lock()
		for i, m := range c.notes {
			if m.Method == method {
				c.notes = append(c.notes[:i], c.notes[i+1:]...)
				c.mu.Unlock()
				return m, nil
			}
		}
		c.mu.Unlock()
		m, _, err := c.Read()
		if err != nil {
			return Message{}, fmt.Errorf("wait for %s: %w", method, err)
		}
		if m.Method == method {
			return m, nil
		}
		c.mu.Lock()
		c.notes = append(c.notes, m)
		c.mu.Unlock()
	}
}

// Close closes the input of the adapter and returns what Serve returned.
func (c *Conn) Close() error {
	c.in.Close() //nolint:errcheck // a pipe close does not fail
	defer c.cancel()
	select {
	case err := <-c.served:
		return err
	case <-time.After(Wait):
		return errors.New("serve did not return after the input closed")
	}
}

// Served waits for Serve to return by itself, as after shutdown.
func (c *Conn) Served() error {
	select {
	case err := <-c.served:
		c.served <- err
		return err
	case <-time.After(Wait):
		return errors.New("serve did not return")
	}
}

// Play runs a transcript: it sends each daemon line with its own request
// id and checks that each adapter line matches the next line the adapter
// writes. AnyValue matches any value.
func (c *Conn) Play(lines []Line) error {
	ids := map[string]string{} // transcript id → sent id
	for i, l := range lines {
		var msg map[string]any
		if err := json.Unmarshal(l.Msg, &msg); err != nil {
			return fmt.Errorf("line %d: %w", i+1, err)
		}
		switch l.From {
		case "daemon":
			if id, ok := msg["id"]; ok {
				c.mu.Lock()
				c.nextID++
				sent := c.nextID
				c.mu.Unlock()
				ids[fmt.Sprint(id)] = strconv.Itoa(sent)
				msg["id"] = sent
			}
			b, err := json.Marshal(msg)
			if err != nil {
				return err
			}
			if err := c.Write(b); err != nil {
				return fmt.Errorf("line %d: %w", i+1, err)
			}
		case "adapter":
			if id, ok := msg["id"]; ok {
				if sent, ok := ids[fmt.Sprint(id)]; ok {
					n, _ := strconv.Atoi(sent) //nolint:errcheck // sent ids are numbers
					msg["id"] = float64(n)
				}
			}
			_, got, err := c.Read()
			if err != nil {
				return fmt.Errorf("line %d: %w", i+1, err)
			}
			var g any
			if err := json.Unmarshal(got, &g); err != nil {
				return fmt.Errorf("line %d: %w", i+1, err)
			}
			if !matchValue(any(msg), g) {
				return fmt.Errorf("line %d:\n want %s\n  got %s", i+1, l.Msg, got)
			}
		default:
			return fmt.Errorf("line %d: from must be daemon or adapter", i+1)
		}
	}
	return nil
}

// LoadTranscript reads a .jsonl transcript.
func LoadTranscript(path string) ([]Line, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Line
	for i, raw := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var l Line
		if err := json.Unmarshal(raw, &l); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
		out = append(out, l)
	}
	return out, nil
}

// Run plays the transcript file at path against a new session of a and
// fails t on the first difference.
func Run(t testing.TB, a sdk.Adapter, path string, opts ...sdk.Option) {
	t.Helper()
	lines, err := LoadTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	c := Start(a, opts...)
	if err := c.Play(lines); err != nil {
		c.Close() //nolint:errcheck // the play error is the one to report
		t.Fatalf("%s: %v", filepath.Base(path), err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("%s: serve: %v", filepath.Base(path), err)
	}
}

// Match compares two JSON values. A want string AnyValue matches any value,
// and an absent value matches {}.
func Match(want, got json.RawMessage) bool {
	var w, g any
	if len(bytes.TrimSpace(want)) == 0 {
		want = json.RawMessage(`{}`)
	}
	if len(bytes.TrimSpace(got)) == 0 {
		got = json.RawMessage(`{}`)
	}
	if json.Unmarshal(want, &w) != nil || json.Unmarshal(got, &g) != nil {
		return false
	}
	return matchValue(w, g)
}

func matchValue(w, g any) bool {
	if s, ok := w.(string); ok && s == AnyValue {
		return true
	}
	wm, wok := w.(map[string]any)
	gm, gok := g.(map[string]any)
	if wok && gok {
		if len(wm) != len(gm) {
			return false
		}
		for k, v := range wm {
			gv, ok := gm[k]
			if !ok || !matchValue(v, gv) {
				return false
			}
		}
		return true
	}
	wa, wok := w.([]any)
	ga, gok := g.([]any)
	if wok && gok {
		if len(wa) != len(ga) {
			return false
		}
		for i := range wa {
			if !matchValue(wa[i], ga[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(w, g)
}

func orEmpty(params any) any {
	if params == nil {
		return sdk.Empty{}
	}
	return params
}
