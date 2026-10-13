// Package serve is the trebi-connector/1 adapter of host-cli. It samples
// one server over SSH at an interval. Each sample is a `sample` event with
// the numbers in data.metrics. sdk.StateWatcher sends state.changed and
// state.recovered when the server goes down or up, when a use goes past a
// limit, and when a service fails.
package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/probe"
	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/sshx"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Name is the adapter name in initialize.
const Name = "host-cli"

// EventSample is the event type of one sample.
const EventSample = "sample"

var (
	_ sdk.Runner     = (*Adapter)(nil)
	_ sdk.FolderUser = (*Adapter)(nil)
)

// Runner runs a command on the server.
type Runner interface {
	Run(ctx context.Context, cmd string) ([]byte, error)
}

// Adapter samples one server.
type Adapter struct {
	r        Runner
	s        config.Settings
	version  string
	stateDir string
	tick     <-chan time.Time
	now      func() time.Time
	setupErr error
	hostname string // of the last sample, for the room name
}

// Option changes the adapter.
type Option func(*Adapter)

// WithEvery replaces the interval with no minimum (sandbox and tests).
func WithEvery(d time.Duration) Option { return func(a *Adapter) { a.s.Every = d } }

// WithTick starts each sample after the first on a value of ch (tests).
func WithTick(ch <-chan time.Time) Option { return func(a *Adapter) { a.tick = ch } }

// WithSetupError reports err as the status, for settings that the client
// cannot use, for example a key with a passphrase.
func WithSetupError(err error) Option { return func(a *Adapter) { a.setupErr = err } }

// WithClock replaces the clock of the sample time (tests).
func WithClock(now func() time.Time) Option { return func(a *Adapter) { a.now = now } }

// New builds the adapter. r is nil when the settings are not complete.
func New(r Runner, s config.Settings, version string, opts ...Option) *Adapter {
	a := &Adapter{r: r, s: s, version: version, now: time.Now}
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
		Account: &sdk.Account{ID: a.s.User + "@" + a.room().ID, Name: a.s.User + "@" + a.room().ID},
		Events:  []sdk.EventDecl{{Type: EventSample}, {Type: sdk.EventStateChanged}, {Type: sdk.EventStateRecovered}},
	}
	if m := a.s.Missing(); m != nil {
		res.Account = nil
		return res, *m
	}
	if a.r == nil && a.setupErr == nil {
		a.setupErr = errors.New("the settings are not complete")
	}
	return res, nil
}

// Run samples the server now and then at each interval.
func (a *Adapter) Run(ctx context.Context, e sdk.Emitter) error {
	w, err := sdk.NewStateWatcher(a.stateDir, e)
	if err == nil {
		err = a.setupErr
	}
	if err != nil {
		if err := e.Status(sdk.Status{State: sdk.StateError, Message: err.Error()}); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	}
	tick := a.tick
	if tick == nil {
		t := time.NewTicker(a.s.Every)
		defer t.Stop()
		tick = t.C
	}
	last := sdk.Status{State: sdk.StateConnected}
	for {
		st, err := a.sample(ctx, e, w)
		if err != nil || ctx.Err() != nil {
			return err
		}
		if st.State != last.State || st.Message != last.Message {
			if err := e.Status(st); err != nil {
				return err
			}
			last = st
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick:
		}
	}
}

// sample reads the server once. It returns the status of the connection.
// err is an emit error.
func (a *Adapter) sample(ctx context.Context, e sdk.Emitter, w *sdk.StateWatcher) (sdk.Status, error) {
	room := a.room()
	at := a.now().UTC()
	rctx, cancel := context.WithTimeout(ctx, min(a.s.Every, time.Minute)+30*time.Second)
	out, rerr := a.r.Run(rctx, probe.Script)
	cancel()
	if ctx.Err() != nil {
		return sdk.Status{}, nil
	}
	var smp probe.Sample
	if rerr == nil {
		smp, rerr = probe.Parse(string(out))
	}
	up := 1
	if rerr != nil {
		up = 0
		slog.Warn("sample", "host", a.s.Addr, "err", rerr)
	} else if smp.Hostname != "" {
		a.hostname = smp.Hostname
	}
	if a.hostname != "" {
		room.Name = a.hostname
	}
	if err := w.Observe(room, "reachable", up, sdk.Below(1), sdk.WithText(func(typ string, _ sdk.StateChange) string {
		if typ == sdk.EventStateRecovered {
			return "Trebi can reach " + room.ID + " again."
		}
		return "Trebi cannot reach " + room.ID + ": " + errText(rerr)
	})); err != nil {
		return sdk.Status{}, err
	}
	if rerr != nil {
		return failStatus(rerr), nil
	}
	if a.s.Samples {
		if err := e.Event(a.sampleEvent(room, at, smp)); err != nil {
			return sdk.Status{}, err
		}
	}
	if err := a.observe(w, room, smp); err != nil {
		return sdk.Status{}, err
	}
	return sdk.Status{State: sdk.StateConnected}, nil
}

// observe sends the threshold crossings and the failed services.
func (a *Adapter) observe(w *sdk.StateWatcher, room sdk.Room, smp probe.Sample) error {
	limit := func(what, key string, v, above float64) error {
		return w.Observe(room, key, v, sdk.Above(above), sdk.WithText(func(typ string, _ sdk.StateChange) string {
			if typ == sdk.EventStateRecovered {
				return fmt.Sprintf("%s on %s is back to %s%%, at or under %s%%.", what, room.Name, num(v), num(above))
			}
			return fmt.Sprintf("%s on %s is %s%%, above %s%%.", what, room.Name, num(v), num(above))
		}))
	}
	if err := limit("CPU use", "cpu.used_pct", smp.CPU, a.s.CPUAbove); err != nil {
		return err
	}
	if err := limit("Memory use", "mem.used_pct", smp.MemUsedPct(), a.s.MemAbove); err != nil {
		return err
	}
	for _, d := range smp.Disks {
		if err := limit("Disk use of "+d.Mount, "disk["+d.Mount+"].used_pct", d.UsedPct, a.s.DiskAbove); err != nil {
			return err
		}
	}
	for _, u := range smp.Units {
		failed := 0
		if u.Failed() {
			failed = 1
		}
		if err := w.Observe(room, "unit["+u.Name+"].failed", failed, sdk.Above(0), sdk.WithText(func(typ string, _ sdk.StateChange) string {
			if typ == sdk.EventStateRecovered {
				return fmt.Sprintf("The service %s on %s runs again (%s).", u.Name, room.Name, u.Active)
			}
			return fmt.Sprintf("The service %s on %s failed.", u.Name, room.Name)
		})); err != nil {
			return err
		}
	}
	return nil
}

type sampleData struct {
	Host        string             `json:"host"`
	Hostname    string             `json:"hostname,omitempty"`
	Metrics     map[string]float64 `json:"metrics"`
	Disks       []probe.Disk       `json:"disks"`
	Processes   []probe.Process    `json:"processes"`
	FailedUnits []string           `json:"failed_units"`
}

func (a *Adapter) sampleEvent(room sdk.Room, at time.Time, smp probe.Sample) sdk.Event {
	d := sampleData{Host: room.ID, Hostname: smp.Hostname, Metrics: smp.Metrics(), Disks: smp.Disks, Processes: smp.Processes, FailedUnits: smp.FailedUnits()}
	if d.Disks == nil {
		d.Disks = []probe.Disk{}
	}
	if d.Processes == nil {
		d.Processes = []probe.Process{}
	}
	if d.FailedUnits == nil {
		d.FailedUnits = []string{}
	}
	data, err := json.Marshal(d)
	if err != nil {
		panic(fmt.Sprintf("marshal sample: %v", err)) // only numbers and strings
	}
	parts := []string{"CPU " + num(smp.CPU) + "%", "memory " + num(smp.MemUsedPct()) + "%"}
	for _, dk := range smp.Disks {
		parts = append(parts, "disk "+dk.Mount+" "+num(dk.UsedPct)+"%")
	}
	parts = append(parts, "load "+num(smp.Load[0]))
	if f := d.FailedUnits; len(f) > 0 {
		parts = append(parts, "failed: "+strings.Join(f, ", "))
	}
	return sdk.Event{
		ID:   "sample:" + room.ID + ":" + strconv.FormatInt(at.Unix(), 10),
		Type: EventSample,
		TS:   at.Format(time.RFC3339),
		Room: &room,
		Text: room.Name + ": " + strings.Join(parts, ", "),
		Data: data,
	}
}

// room is the server: the address with no default port.
func (a *Adapter) room() sdk.Room {
	id := a.s.Addr
	if h, p, err := net.SplitHostPort(id); err == nil && p == "22" {
		id = h
	}
	return sdk.Room{ID: id, Name: id, Kind: "host"}
}

func failStatus(err error) sdk.Status {
	switch {
	case errors.Is(err, sshx.ErrAuth):
		return sdk.Status{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked, Message: "The server refused the key. Check the user name and the private key."}
	case errors.Is(err, sshx.ErrHostKey):
		return sdk.Status{State: sdk.StateError, Message: "The server key is not the known key. If the server was set up again, set the server key in the connection. " + err.Error()}
	}
	return sdk.Status{State: sdk.StateError, Message: errText(err)}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
