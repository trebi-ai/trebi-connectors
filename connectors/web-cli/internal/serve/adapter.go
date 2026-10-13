// Package serve is the trebi-connector/1 adapter of web-cli. It reads each
// address at an interval. A feed gives one entry event for each new entry.
// A page gives state.changed when its text changes.
package serve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/web-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/web-cli/internal/watch"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Name is the adapter name in initialize.
const Name = "web-cli"

// EventEntry is the event type of a new feed entry.
const EventEntry = "entry"

// maxText caps the page text that the state keeps. A longer text keeps
// its start and a digest of the full text, so a change at the end shows.
const maxText = 8000

var (
	_ sdk.Runner     = (*Adapter)(nil)
	_ sdk.FolderUser = (*Adapter)(nil)
)

// Reader reads one address.
type Reader interface {
	Read(ctx context.Context, url, selector string) (watch.Result, error)
}

// Adapter watches the targets of one connection.
type Adapter struct {
	r        Reader
	targets  []config.Target
	every    time.Duration
	version  string
	stateDir string
	tick     <-chan time.Time
}

// Option changes the adapter.
type Option func(*Adapter)

// WithEvery replaces the interval with no minimum (sandbox and tests).
func WithEvery(d time.Duration) Option { return func(a *Adapter) { a.every = d } }

// WithTick starts each round after the first on a value of ch (tests).
func WithTick(ch <-chan time.Time) Option { return func(a *Adapter) { a.tick = ch } }

// New builds the adapter.
func New(r Reader, s config.Settings, version string, opts ...Option) *Adapter {
	a := &Adapter{r: r, targets: s.Targets, every: s.Every, version: version}
	for _, o := range opts {
		o(a)
	}
	return a
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
		Events:  []sdk.EventDecl{{Type: EventEntry}, {Type: sdk.EventStateChanged}},
	}
	if len(a.targets) == 0 {
		return res, sdk.MissingInput{Name: config.EnvURLs, Label: config.URLsLabel}
	}
	return res, nil
}

// Run reads each target now and then at each interval.
func (a *Adapter) Run(ctx context.Context, e sdk.Emitter) error {
	w, err := sdk.NewStateWatcher(a.stateDir, e)
	if err == nil {
		var fs *feedStore
		if fs, err = loadFeeds(a.stateDir); err == nil {
			return a.loop(ctx, e, w, fs)
		}
	}
	if err := e.Status(sdk.Status{State: sdk.StateError, Message: err.Error()}); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func (a *Adapter) loop(ctx context.Context, e sdk.Emitter, w *sdk.StateWatcher, fs *feedStore) error {
	tick := a.tick
	if tick == nil {
		t := time.NewTicker(a.every)
		defer t.Stop()
		tick = t.C
	}
	failing := false
	for {
		failed, firstErr, err := a.round(ctx, e, w, fs)
		if err != nil || ctx.Err() != nil {
			return err
		}
		switch {
		case failed == len(a.targets) && !failing:
			failing = true
			if err := e.Status(sdk.Status{State: sdk.StateError, Message: "Trebi cannot read the addresses: " + firstErr.Error()}); err != nil {
				return err
			}
		case failed < len(a.targets) && failing:
			failing = false
			if err := e.Status(sdk.Status{State: sdk.StateConnected}); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick:
		}
	}
}

// round reads each target once. It returns the count of targets that
// failed and the first read error. err is an emit or a state error.
func (a *Adapter) round(ctx context.Context, e sdk.Emitter, w *sdk.StateWatcher, fs *feedStore) (failed int, firstErr, err error) {
	for _, t := range a.targets {
		res, rerr := a.r.Read(ctx, t.URL, t.Selector)
		if ctx.Err() != nil {
			return failed, firstErr, nil
		}
		if rerr != nil {
			slog.Warn("read", "url", t.URL, "err", rerr)
			failed++
			if firstErr == nil {
				firstErr = rerr
			}
			continue
		}
		if res.Feed != nil {
			err = a.feed(e, fs, t, *res.Feed)
		} else {
			err = a.page(w, t, *res.Page)
		}
		if err != nil {
			return failed, firstErr, err
		}
	}
	return failed, firstErr, nil
}

// feed emits the entries that the store has not seen, oldest first. The
// first read of a feed only marks its entries as seen.
func (a *Adapter) feed(e sdk.Emitter, fs *feedStore, t config.Target, f watch.Feed) error {
	fresh, primed := fs.unseen(t.URL, f.Entries)
	if primed {
		room := &sdk.Room{ID: t.URL, Name: f.Title, Kind: "feed"}
		if room.Name == "" {
			room.Name = t.URL
		}
		for i := len(fresh) - 1; i >= 0; i-- {
			if err := e.Event(entryEvent(t.URL, room, f, fresh[i])); err != nil {
				return err
			}
		}
	}
	return fs.mark(t.URL, f.Entries)
}

// page observes the text of the page.
func (a *Adapter) page(w *sdk.StateWatcher, t config.Target, p watch.Page) error {
	name := p.Title
	if name == "" {
		name = t.URL
	}
	key := "text"
	if t.Selector != "" {
		key = "text " + t.Selector
	}
	return w.Observe(sdk.Room{ID: t.URL, Name: name, Kind: "page"}, key, stateText(p.Text), sdk.WithText(func(string, sdk.StateChange) string {
		return "The page " + name + " changed.\n" + t.URL
	}))
}

// stateText is the value that the state keeps for a page text.
func stateText(s string) string {
	if len(s) <= maxText {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return s[:maxText] + "\n… sha256:" + hex.EncodeToString(sum[:8])
}

type entryData struct {
	Feed      string `json:"feed"`
	FeedTitle string `json:"feed_title,omitempty"`
	watch.Entry
}

func entryEvent(url string, room *sdk.Room, f watch.Feed, en watch.Entry) sdk.Event {
	sum := sha256.Sum256([]byte(url + "\n" + en.ID))
	ts := en.Published
	if ts == "" {
		ts = time.Now().UTC().Format(time.RFC3339)
	}
	text := en.Title
	if en.Link != "" {
		text += "\n" + en.Link
	}
	data, err := json.Marshal(entryData{Feed: url, FeedTitle: f.Title, Entry: en})
	if err != nil {
		panic(fmt.Sprintf("marshal entry: %v", err)) // only strings
	}
	ev := sdk.Event{ID: "entry:" + hex.EncodeToString(sum[:12]), Type: EventEntry, TS: ts, Room: room, Text: text, Data: data}
	if en.Author != "" {
		ev.Sender = &sdk.Author{ID: en.Author, Name: en.Author}
	}
	return ev
}
