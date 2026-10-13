package sdk

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"
)

// StateFile is the store of StateWatcher in TREBI_STATE_DIR.
const StateFile = "trebi-sdk-state.json"

// Event types of StateWatcher. Declare them in InitializeResult.Events.
const (
	EventStateChanged   = "state.changed"
	EventStateRecovered = "state.recovered"
)

// Threshold operators.
const (
	ThresholdAbove = "above"
	ThresholdBelow = "below"
)

// EventEmitter sends events. Emitter satisfies it.
type EventEmitter interface {
	Event(e Event) error
}

// Threshold is one numeric limit of a key.
type Threshold struct {
	Op    string  `json:"op"`
	Value float64 `json:"value"`
}

func (t Threshold) breached(v float64) bool {
	if t.Op == ThresholdAbove {
		return v > t.Value
	}
	return v < t.Value
}

func (t Threshold) id() string {
	return t.Op + ":" + strconv.FormatFloat(t.Value, 'g', -1, 64)
}

// StateChange is the data of a state.changed or state.recovered event. Old
// is null for the first value of a key.
type StateChange struct {
	Key       string          `json:"key"`
	Old       json.RawMessage `json:"old"`
	New       json.RawMessage `json:"new"`
	Threshold *Threshold      `json:"threshold,omitempty"`
}

// StateOption changes one Observe call.
type StateOption func(*observeConfig)

type observeConfig struct {
	thresholds []Threshold
	text       func(typ string, c StateChange) string
}

// Above emits once when the value goes above n, and state.recovered when it
// goes back to n or less.
func Above(n float64) StateOption {
	return func(c *observeConfig) { c.thresholds = append(c.thresholds, Threshold{Op: ThresholdAbove, Value: n}) }
}

// Below emits once when the value goes below n, and state.recovered when it
// goes back to n or more.
func Below(n float64) StateOption {
	return func(c *observeConfig) { c.thresholds = append(c.thresholds, Threshold{Op: ThresholdBelow, Value: n}) }
}

// WithText sets the text of each event. typ is EventStateChanged or
// EventStateRecovered.
func WithText(f func(typ string, c StateChange) string) StateOption {
	return func(c *observeConfig) { c.text = f }
}

// StateWatcher keeps the last value of each key per room and emits an
// event only on a change. Without a threshold, each new value emits
// state.changed, but the first value of a key emits nothing. With
// thresholds, only a crossing emits: state.changed on the way out and
// state.recovered on the way back. With a stateDir, the values stay in
// StateFile, so a restart does not emit each key again.
type StateWatcher struct {
	path string
	emit EventEmitter
	mu   sync.Mutex
	data stateDoc
}

// stateVersion is the format of StateFile.
const stateVersion = 1

type stateDoc struct {
	Version int                               `json:"version"`
	Rooms   map[string]map[string]*stateEntry `json:"rooms"`
}

type stateEntry struct {
	Value json.RawMessage `json:"value"`
	// Breached holds the ids of the thresholds that the value is past.
	Breached []string `json:"breached,omitempty"`
	// Seq counts the emitted events, so each event id is new and stable.
	Seq int `json:"seq"`
}

// NewStateWatcher loads the store in stateDir. An empty stateDir keeps the
// values in memory.
func NewStateWatcher(stateDir string, e EventEmitter) (*StateWatcher, error) {
	w := &StateWatcher{emit: e, data: stateDoc{Version: stateVersion, Rooms: map[string]map[string]*stateEntry{}}}
	if stateDir == "" {
		return w, nil
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("state dir: %w", err)
	}
	w.path = filepath.Join(stateDir, StateFile)
	b, err := os.ReadFile(w.path)
	if errors.Is(err, os.ErrNotExist) {
		return w, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", w.path, err)
	}
	if err := json.Unmarshal(b, &w.data); err != nil {
		return nil, fmt.Errorf("read %s: %w", w.path, err)
	}
	if w.data.Version > stateVersion {
		return nil, fmt.Errorf("%s has format %d; this program reads format %d or older", w.path, w.data.Version, stateVersion)
	}
	w.data.Version = stateVersion
	if w.data.Rooms == nil {
		w.data.Rooms = map[string]map[string]*stateEntry{}
	}
	return w, nil
}

// Observe records the value of key in room and emits the events of the
// change. value must encode as JSON; with a threshold, it must be a number.
// When an emit fails, the old value stays, so the next Observe tries again.
func (w *StateWatcher) Observe(room Room, key string, value any, opts ...StateOption) error {
	if room.ID == "" || key == "" {
		return Invalid("state needs a room id and a key")
	}
	var cfg observeConfig
	for _, o := range opts {
		o(&cfg)
	}
	val, err := json.Marshal(value)
	if err != nil {
		return Invalid("state value of " + strconv.Quote(key) + ": " + err.Error())
	}
	var num float64
	if len(cfg.thresholds) > 0 {
		if err := json.Unmarshal(val, &num); err != nil {
			return Invalid("state value of " + strconv.Quote(key) + " is not a number")
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	keys := w.data.Rooms[room.ID]
	if keys == nil {
		keys = map[string]*stateEntry{}
		w.data.Rooms[room.ID] = keys
	}
	prev := keys[key]
	if prev != nil && bytes.Equal(prev.Value, val) {
		return nil
	}
	next := &stateEntry{Value: val}
	var old json.RawMessage = []byte("null")
	if prev != nil {
		old = prev.Value
		next.Seq = prev.Seq
	}

	var events []Event
	add := func(typ string, t *Threshold) {
		c := StateChange{Key: key, Old: old, New: val, Threshold: t}
		next.Seq++
		events = append(events, w.event(room, typ, c, next.Seq, cfg.text))
	}
	if len(cfg.thresholds) == 0 {
		if prev != nil {
			add(EventStateChanged, nil)
		}
	} else {
		for _, t := range cfg.thresholds {
			was := prev != nil && slices.Contains(prev.Breached, t.id())
			now := t.breached(num)
			if now {
				next.Breached = append(next.Breached, t.id())
			}
			switch {
			case now && !was:
				add(EventStateChanged, &t)
			case !now && was:
				add(EventStateRecovered, &t)
			}
		}
	}

	for _, ev := range events {
		if err := w.emit.Event(ev); err != nil {
			return err
		}
	}
	keys[key] = next
	return w.save()
}

// event builds one event. The id comes from the room, the key, and the
// sequence, so an emit again after a crash has the same id.
func (w *StateWatcher) event(room Room, typ string, c StateChange, seq int, text func(string, StateChange) string) Event {
	h := sha256.Sum256([]byte(room.ID + "\x00" + c.Key + "\x00" + strconv.Itoa(seq)))
	data, _ := json.Marshal(c) //nolint:errcheck // fields are valid JSON
	ev := Event{
		ID:   "state-" + hex.EncodeToString(h[:12]),
		Type: typ,
		TS:   FormatTime(time.Now()),
		Room: &room,
		Data: data,
	}
	if text != nil {
		ev.Text = text(typ, c)
	}
	return ev
}

// save writes the store with a rename, so a crash never leaves half a file.
func (w *StateWatcher) save() error {
	if w.path == "" {
		return nil
	}
	b, err := json.Marshal(w.data)
	if err != nil {
		return err
	}
	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, w.path)
}
