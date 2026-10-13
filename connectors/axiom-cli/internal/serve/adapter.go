// Package serve is the trebi-connector/1 adapter of axiom-cli. It polls the
// history of each Axiom monitor and sends state.changed when a monitor
// triggers and state.recovered when it resolves.
package serve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Name is the adapter name in initialize.
const Name = "axiom-cli"

// maxBack bounds the history window after a long stop.
const maxBack = 7 * 24 * time.Hour

var (
	_ sdk.Runner     = (*Adapter)(nil)
	_ sdk.FolderUser = (*Adapter)(nil)
)

// API reads the Axiom API.
type API interface {
	Get(ctx context.Context, path string, q url.Values) (json.RawMessage, error)
}

// Adapter polls the monitors of one Axiom organization.
type Adapter struct {
	api      API
	missing  bool
	every    time.Duration
	version  string
	stateDir string
	tick     <-chan time.Time
	now      func() time.Time
}

// Option changes the adapter.
type Option func(*Adapter)

// WithEvery replaces the interval with no minimum (sandbox and tests).
func WithEvery(d time.Duration) Option { return func(a *Adapter) { a.every = d } }

// WithTick starts each poll after the first on a value of ch (tests).
func WithTick(ch <-chan time.Time) Option { return func(a *Adapter) { a.tick = ch } }

// WithClock replaces the clock (tests).
func WithClock(now func() time.Time) Option { return func(a *Adapter) { a.now = now } }

// New builds the adapter. A nil api means that the token is missing.
func New(api API, every time.Duration, version string, opts ...Option) *Adapter {
	a := &Adapter{api: api, missing: api == nil, every: every, version: version, now: time.Now}
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

// Initialize reports a missing token.
func (a *Adapter) Initialize(context.Context, sdk.InitializeParams) (sdk.InitializeResult, error) {
	res := sdk.InitializeResult{
		Adapter: sdk.AdapterInfo{Name: Name, Version: a.version},
		Events:  []sdk.EventDecl{{Type: sdk.EventStateChanged}, {Type: sdk.EventStateRecovered}},
	}
	if a.missing {
		return res, sdk.MissingInput{Name: config.EnvToken, Label: config.TokenLabel}
	}
	return res, nil
}

// Run polls now and then at each interval.
func (a *Adapter) Run(ctx context.Context, e sdk.Emitter) error {
	st, err := loadStore(a.stateDir)
	if err != nil {
		if err := e.Status(sdk.Status{State: sdk.StateError, Message: err.Error()}); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	}
	tick := a.tick
	if tick == nil {
		t := time.NewTicker(a.every)
		defer t.Stop()
		tick = t.C
	}
	last := sdk.Status{State: sdk.StateConnected}
	for {
		status, err := a.poll(ctx, e, st)
		if err != nil || ctx.Err() != nil {
			return err
		}
		if status.State != last.State || status.Message != last.Message {
			if err := e.Status(status); err != nil {
				return err
			}
			last = status
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick:
		}
	}
}

// monitor is the part of a monitor that the adapter uses.
type monitor struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Datasets []string `json:"datasets"`
	APLQuery string   `json:"aplQuery"`
}

// check is one history entry.
type check struct {
	CheckID   string `json:"checkId"`
	Name      string `json:"name"`
	State     string `json:"state"`
	Timestamp string `json:"timestamp"`
	at        time.Time
}

func (c check) key() string { return c.CheckID + "\x00" + c.Timestamp + "\x00" + c.State }

// poll reads each monitor once. It returns the status of the connection.
// err is an emit or a state write error.
func (a *Adapter) poll(ctx context.Context, e sdk.Emitter, st *store) (sdk.Status, error) {
	raw, err := a.api.Get(ctx, "/v2/monitors", nil)
	if err != nil {
		return a.failStatus(ctx, err), nil
	}
	var mons []monitor
	if err := json.Unmarshal(raw, &mons); err != nil {
		return sdk.Status{State: sdk.StateError, Message: "Axiom gave a monitor list that Trebi cannot read: " + err.Error()}, nil
	}
	now := a.now().UTC()
	ids := make([]string, 0, len(mons))
	var firstErr error
	for _, m := range mons {
		ids = append(ids, m.ID)
		if err := a.pollMonitor(ctx, e, st, m, now); err != nil {
			var emitErr emitError
			if errors.As(err, &emitErr) {
				return sdk.Status{}, emitErr.err
			}
			if ctx.Err() != nil {
				return sdk.Status{}, nil
			}
			slog.Warn("monitor history", "monitor", m.ID, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if err := st.keep(ids); err != nil {
		return sdk.Status{}, err
	}
	if firstErr != nil {
		return a.failStatus(ctx, firstErr), nil
	}
	return sdk.Status{State: sdk.StateConnected}, nil
}

type emitError struct{ err error }

func (e emitError) Error() string { return e.err.Error() }

// pollMonitor sends the new history entries of one monitor, oldest first.
// The first sight of a monitor only records the time.
func (a *Adapter) pollMonitor(ctx context.Context, e sdk.Emitter, st *store, m monitor, now time.Time) error {
	cur := st.get(m.ID)
	if cur == nil {
		return st.put(m.ID, &cursor{Since: now.Format(time.RFC3339Nano)})
	}
	since, err := time.Parse(time.RFC3339Nano, cur.Since)
	if err != nil {
		since = now
	}
	from := since
	if now.Sub(from) > maxBack {
		from = now.Add(-maxBack)
	}
	q := url.Values{"startTime": {from.Format(time.RFC3339Nano)}, "endTime": {now.Format(time.RFC3339Nano)}}
	raw, err := a.api.Get(ctx, "/v2/monitors/"+url.PathEscape(m.ID)+"/history", q)
	if err != nil {
		return err
	}
	var checks []check
	if err := json.Unmarshal(raw, &checks); err != nil {
		return fmt.Errorf("history of %s: %w", m.Name, err)
	}
	fresh := checks[:0]
	for _, c := range checks {
		at, err := time.Parse(time.RFC3339Nano, c.Timestamp)
		if err != nil || (c.State != "open" && c.State != "closed") {
			continue
		}
		c.at = at
		if at.Before(since) || at.Equal(since) && slices.Contains(cur.Seen, c.key()) {
			continue
		}
		fresh = append(fresh, c)
	}
	sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].at.Before(fresh[j].at) })
	for _, c := range fresh {
		if err := e.Event(a.event(m, c, cur.State)); err != nil {
			return emitError{err}
		}
		if !c.at.Equal(since) {
			since, cur.Seen = c.at, nil
		}
		cur.Since, cur.State = c.Timestamp, c.State
		cur.Seen = append(cur.Seen, c.key())
		if err := st.put(m.ID, cur); err != nil {
			return emitError{err}
		}
	}
	return nil
}

// Data is the data of a monitor event.
type Data struct {
	Key       string   `json:"key"`
	Old       *string  `json:"old"`
	New       string   `json:"new"`
	MonitorID string   `json:"monitor_id"`
	Monitor   string   `json:"monitor"`
	Type      string   `json:"type,omitempty"`
	Datasets  []string `json:"datasets,omitempty"`
	Query     string   `json:"query,omitempty"`
	CheckID   string   `json:"check_id,omitempty"`
	At        string   `json:"at"`
}

func (a *Adapter) event(m monitor, c check, old string) sdk.Event {
	d := Data{Key: "state", New: c.State, MonitorID: m.ID, Monitor: m.Name, Type: m.Type, Datasets: m.Datasets, Query: m.APLQuery, CheckID: c.CheckID, At: c.Timestamp}
	if old != "" {
		d.Old = &old
	}
	data, err := json.Marshal(d)
	if err != nil {
		panic(fmt.Sprintf("marshal monitor event: %v", err)) // only strings
	}
	typ, text := sdk.EventStateChanged, "The Axiom monitor "+m.Name+" triggered."
	if c.State == "closed" {
		typ, text = sdk.EventStateRecovered, "The Axiom monitor "+m.Name+" resolved."
	}
	h := sha256.Sum256([]byte(m.ID + "\x00" + c.key()))
	return sdk.Event{
		ID:   "axiom-" + hex.EncodeToString(h[:12]),
		Type: typ,
		TS:   sdk.FormatTime(c.at),
		Room: &sdk.Room{ID: m.ID, Name: m.Name, Kind: "monitor"},
		Text: text,
		Data: data,
	}
}

func (a *Adapter) failStatus(ctx context.Context, err error) sdk.Status {
	if ctx.Err() != nil {
		return sdk.Status{}
	}
	var api *client.APIError
	if errors.As(err, &api) && (api.Status == http.StatusUnauthorized || api.Status == http.StatusForbidden) {
		return sdk.Status{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked, Message: "Axiom refused the API token. Set a new token that can read monitors."}
	}
	return sdk.Status{State: sdk.StateError, Message: "Trebi cannot read the Axiom monitors: " + err.Error()}
}
