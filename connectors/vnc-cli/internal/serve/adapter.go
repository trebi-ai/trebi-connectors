// Package serve is the trebi-connector/1 adapter of vnc-cli. It holds the
// connection to one computer, runs the commands of jobs that come on the
// socket in the state folder, and sends the watch events.
package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/watch"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Name is the adapter name in initialize.
const Name = "vnc-cli"

// Event types.
const (
	EventChanged   = "screen.changed"
	EventStable    = "screen.stable"
	EventClipboard = "clipboard.changed"
	EventBell      = "bell"
	EventResized   = "screen.resized"
)

// DefaultSettle is the quiet time before a stable event.
const DefaultSettle = 700 * time.Millisecond

// keepImages is the number of pictures that stay in the images folder.
const keepImages = 200

var (
	_ sdk.Runner         = (*Adapter)(nil)
	_ sdk.FolderUser     = (*Adapter)(nil)
	_ sdk.StatusReporter = (*Adapter)(nil)
)

// Holder keeps the connection and serves the socket.
type Holder interface {
	watch.Source
	OnChange(func(error))
	Serve(ctx context.Context, ln net.Listener) error
}

// Adapter holds one computer.
type Adapter struct {
	h        Holder
	ln       net.Listener
	s        config.Settings
	version  string
	spec     watch.Spec
	images   bool
	stateDir string
	setupErr error

	mu    sync.Mutex
	name  string        // of the desktop, for the room
	state sdk.AuthState // of the connection
}

// New builds the adapter. h and ln are nil when the settings are not
// complete.
func New(h Holder, ln net.Listener, s config.Settings, version string) *Adapter {
	a := &Adapter{h: h, ln: ln, s: s, version: version, state: sdk.AuthState{State: sdk.StateConnecting}}
	a.spec, a.images, a.setupErr = Spec(s)
	return a
}

// Spec parses the watch inputs.
func Spec(s config.Settings) (watch.Spec, bool, error) {
	spec := watch.Spec{Options: watch.Options{Threshold: 0.5, MinPixels: 16, PixelDelta: 24, Settle: DefaultSettle, MaxFPS: 10}}
	for i, f := range strings.FieldsFunc(s.Areas, func(r rune) bool { return r == ';' || r == '\n' }) {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		ar, err := watch.ParseArea(f, i+1)
		if err != nil {
			return spec, false, fmt.Errorf("%s: %w", config.AreasLabel, err)
		}
		spec.Areas = append(spec.Areas, ar)
	}
	if s.Settle != "" {
		d, err := time.ParseDuration(s.Settle)
		if err != nil || d < 0 {
			return spec, false, fmt.Errorf("%s: %q is not a time such as 700ms or 2s", config.SettleLabel, s.Settle)
		}
		spec.Options.Settle = d
	}
	switch strings.ToLower(s.Images) {
	case "", "yes":
		return spec, true, nil
	case "no":
		return spec, false, nil
	}
	return spec, false, fmt.Errorf("%s: %q is not yes or no", config.ImagesLabel, s.Images)
}

// UseFolders keeps the state folder.
func (a *Adapter) UseFolders(t sdk.Trebi) error {
	a.stateDir = t.StateDir
	return nil
}

// Initialize reports a missing input.
func (a *Adapter) Initialize(context.Context, sdk.InitializeParams) (sdk.InitializeResult, error) {
	res := sdk.InitializeResult{
		Adapter: sdk.AdapterInfo{Name: Name, Version: a.version},
		Events: []sdk.EventDecl{
			{Type: EventChanged}, {Type: EventStable}, {Type: EventClipboard}, {Type: EventBell}, {Type: EventResized},
			{Type: sdk.EventStateChanged}, {Type: sdk.EventStateRecovered},
		},
	}
	if m := a.s.Missing(); m != nil {
		return res, *m
	}
	id := a.s.Server
	if a.s.User != "" {
		id = a.s.User + "@" + id
	}
	res.Account = &sdk.Account{ID: id, Name: id}
	return res, nil
}

// AuthStatus is the first status: connecting until the first connect.
func (a *Adapter) AuthStatus(context.Context) (sdk.AuthState, error) {
	if a.setupErr != nil {
		return sdk.AuthState{}, a.setupErr
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state, nil
}

// Run holds the connection and sends the events until ctx ends. A failed
// login stops the connection, so that a wrong password is not sent again.
func (a *Adapter) Run(ctx context.Context, e sdk.Emitter) error {
	if a.setupErr != nil {
		// The first status tells it.
		<-ctx.Done()
		return nil
	}
	w, err := sdk.NewStateWatcher(a.stateDir, e)
	if err != nil {
		return a.fail(ctx, e, err)
	}
	last := sdk.Status{State: sdk.StateConnecting}
	a.h.OnChange(func(cerr error) {
		if st := a.status(cerr); st.State != last.State || st.Message != last.Message {
			last = st
			a.mu.Lock()
			a.state = sdk.AuthState{State: st.State, Reason: st.Reason}
			a.mu.Unlock()
			if err := e.Status(st); err != nil {
				slog.Warn("status", "err", err)
			}
		}
		up := 0
		if cerr == nil {
			up = 1
		}
		room := a.room()
		if err := w.Observe(room, "reachable", up, sdk.Below(1), sdk.WithText(func(typ string, _ sdk.StateChange) string {
			if typ == sdk.EventStateRecovered {
				return "Trebi can reach " + room.Name + " again."
			}
			return "Trebi cannot reach " + room.Name + ": " + cerr.Error()
		})); err != nil {
			slog.Warn("state", "err", err)
		}
	})

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- a.h.Serve(wctx, a.ln) }()
	watched := make(chan error, 1)
	go func() {
		watched <- watch.Run(wctx, a.h, a.spec, func(ev watch.Event) error { return a.event(wctx, e, ev) })
	}()

	select {
	case err = <-served:
		cancel()
		<-watched
	case err = <-watched:
		cancel()
		<-served
		if err != nil && ctx.Err() == nil {
			return a.fail(ctx, e, err)
		}
	}
	if ctx.Err() != nil || errors.Is(err, vnc.ErrAuth) {
		// The status tells the login fault. Wait for new inputs.
		<-ctx.Done()
		return nil
	}
	return err
}

// fail reports err and waits for ctx.
func (a *Adapter) fail(ctx context.Context, e sdk.Emitter, err error) error {
	if err := e.Status(sdk.Status{State: sdk.StateError, Message: err.Error()}); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func (a *Adapter) status(err error) sdk.Status {
	switch {
	case err == nil:
		return sdk.Status{State: sdk.StateConnected}
	case errors.Is(err, vnc.ErrAuth) && a.s.Password == "":
		return sdk.Status{State: sdk.StateAuthRequired, Reason: sdk.ReasonMissingInput, Message: "The computer asks for a password. Set " + config.PasswordLabel + "."}
	case errors.Is(err, vnc.ErrAuth):
		return sdk.Status{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked, Message: "The computer refused the login. Check " + config.PasswordLabel + " and " + config.UserLabel + "."}
	}
	return sdk.Status{State: sdk.StateError, Message: "Trebi cannot reach " + a.s.Server + " and tries again: " + err.Error()}
}

// room is the computer: the address, with the desktop name when known.
func (a *Adapter) room() sdk.Room {
	a.mu.Lock()
	defer a.mu.Unlock()
	name := a.name
	if name == "" {
		name = a.s.Server
	}
	return sdk.Room{ID: a.s.Server, Name: name, Kind: "computer"}
}

func (a *Adapter) event(ctx context.Context, e sdk.Emitter, ev watch.Event) error {
	room := a.room()
	var typ, text string
	switch ev.Event {
	case watch.KindReady:
		if c, err := a.h.Conn(ctx); err == nil && c.Name() != "" {
			a.mu.Lock()
			a.name = c.Name()
			a.mu.Unlock()
		}
		return nil
	case watch.KindChange:
		typ, text = EventChanged, fmt.Sprintf("The screen of %s changes in %q: %.2f%% of the area.", room.Name, ev.Area, ev.Changed)
	case watch.KindStable:
		typ, text = EventStable, fmt.Sprintf("The screen of %s stopped changing in %q: %.2f%% of the area is different.", room.Name, ev.Area, ev.Changed)
	case watch.KindClipboard:
		typ, text = EventClipboard, fmt.Sprintf("The clipboard of %s changed: %s", room.Name, short(ev.Text, 200))
	case watch.KindBell:
		typ, text = EventBell, room.Name+" rang the bell."
	case watch.KindResize:
		typ, text = EventResized, fmt.Sprintf("The screen of %s is now %dx%d.", room.Name, ev.Size.Width, ev.Size.Height)
	default:
		// The status and state.changed tell the connection.
		return nil
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	out := sdk.Event{
		ID:   fmt.Sprintf("%s:%s:%s:%d", typ, room.ID, ev.Area, ev.Time.UnixNano()),
		Type: typ, TS: ev.Time.UTC().Format(time.RFC3339Nano), Room: &room, Text: text, Data: data,
	}
	if typ == EventStable && a.images && ev.Img != nil {
		at, err := a.save(ev)
		if err != nil {
			slog.Warn("save the picture", "err", err)
		} else {
			out.Attachments = []sdk.Attachment{at}
		}
	}
	return e.Event(out)
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// save writes the picture of the area to the images folder and keeps the
// newest keepImages.
func (a *Adapter) save(ev watch.Event) (sdk.Attachment, error) {
	dir := filepath.Join(a.stateDir, "images")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return sdk.Attachment{}, err
	}
	var b bytes.Buffer
	if err := png.Encode(&b, ev.Img); err != nil {
		return sdk.Attachment{}, err
	}
	name := fmt.Sprintf("%020d-%s.png", ev.Time.UnixNano(), unsafeName.ReplaceAllString(ev.Area, "_"))
	path := filepath.Join(dir, name)
	if err := sdk.WriteFileAtomic(path, b.Bytes(), 0o600); err != nil {
		return sdk.Attachment{}, err
	}
	if names, err := filepath.Glob(filepath.Join(dir, "*.png")); err == nil && len(names) > keepImages {
		sort.Strings(names)
		for _, old := range names[:len(names)-keepImages] {
			os.Remove(old)
		}
	}
	return sdk.Attachment{ID: name, Name: name, Mime: "image/png", Size: int64(b.Len()), Path: path}, nil
}

func short(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
