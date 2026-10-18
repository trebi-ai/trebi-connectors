package sdk

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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

// parseThreshold reads an id of Threshold.id.
func parseThreshold(id string) (Threshold, bool) {
	op, v, ok := strings.Cut(id, ":")
	n, err := strconv.ParseFloat(v, 64)
	return Threshold{Op: op, Value: n}, ok && err == nil
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
// state.recovered on the way back. When a threshold changes, an open breach
// moves to a new threshold with the same operator, or recovers. With a
// stateDir, the values stay in StateFile, so a restart does not emit each
// key again.
type StateWatcher struct {
	path  string
	emit  EventEmitter
	mu    sync.Mutex
	data  stateDoc
	rooms map[string]Room // the last room of each id, for the events of Retain
}

// stateVersion is the format of StateFile.
const stateVersion = 1

type stateDoc struct {
	Version int `json:"version"`
	// Epoch is random for each new store, so the event ids of a new store
	// never repeat the ids of a lost one.
	Epoch string                            `json:"epoch"`
	Rooms map[string]map[string]*stateEntry `json:"rooms"`
}

type stateEntry struct {
	Value json.RawMessage `json:"value"`
	// Limits holds the ids of the thresholds of the last Observe.
	Limits []string `json:"limits,omitempty"`
	// Breached holds the ids of the thresholds that the value is past.
	Breached []string `json:"breached,omitempty"`
	// Seq counts the emitted events, so each event id is new and stable.
	Seq int `json:"seq"`
}

// NewStateWatcher loads the store in stateDir. An empty stateDir keeps the
// values in memory.
func NewStateWatcher(stateDir string, e EventEmitter) (*StateWatcher, error) {
	w := &StateWatcher{emit: e, rooms: map[string]Room{}, data: stateDoc{Version: stateVersion, Rooms: map[string]map[string]*stateEntry{}}}
	if stateDir == "" {
		w.data.Epoch = newEpoch()
		return w, nil
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("state dir: %w", err)
	}
	w.path = filepath.Join(stateDir, StateFile)
	b, err := os.ReadFile(w.path)
	if errors.Is(err, os.ErrNotExist) {
		w.data.Epoch = newEpoch()
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
	if w.data.Epoch == "" {
		w.data.Epoch = newEpoch()
	}
	return w, nil
}

func newEpoch() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) //nolint:errcheck // crypto/rand never fails
	return hex.EncodeToString(b)
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
	w.rooms[room.ID] = room
	keys := w.data.Rooms[room.ID]
	if keys == nil {
		keys = map[string]*stateEntry{}
		w.data.Rooms[room.ID] = keys
	}
	prev := keys[key]
	limits := make([]string, len(cfg.thresholds))
	for i, t := range cfg.thresholds {
		limits[i] = t.id()
	}
	if prev != nil && bytes.Equal(prev.Value, val) && slices.Equal(prev.Limits, limits) {
		return nil
	}
	next := &stateEntry{Value: val}
	var old json.RawMessage = []byte("null")
	if prev != nil {
		old = prev.Value
		next.Seq = prev.Seq
	}
	if len(limits) > 0 {
		next.Limits = limits
	}

	var events []Event
	add := func(typ string, t *Threshold) {
		c := StateChange{Key: key, Old: old, New: val, Threshold: t}
		next.Seq++
		events = append(events, w.event(room, typ, c, next.Seq, cfg.text))
	}
	// stale holds the breaches of thresholds that this call does not give.
	var stale []Threshold
	if prev != nil {
		for _, id := range prev.Breached {
			if t, ok := parseThreshold(id); ok && !slices.Contains(limits, id) {
				stale = append(stale, t)
			}
		}
	}
	if len(cfg.thresholds) == 0 && prev != nil && !bytes.Equal(prev.Value, val) {
		add(EventStateChanged, nil)
	}
	for _, t := range cfg.thresholds {
		was := prev != nil && slices.Contains(prev.Breached, t.id())
		now := t.breached(num)
		if now && !was {
			if i := slices.IndexFunc(stale, func(s Threshold) bool { return s.Op == t.Op }); i >= 0 {
				stale = slices.Delete(stale, i, i+1)
				was = true
			}
		}
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
	for _, t := range stale {
		add(EventStateRecovered, &t)
	}

	for _, ev := range events {
		if err := w.emit.Event(ev); err != nil {
			return err
		}
	}
	keys[key] = next
	return w.save()
}

// Retain drops each key that keep does not list, and each room that keep
// does not have. A dropped key with an open breach emits state.recovered
// with a null new value, in the room of the last Observe. Call it after a full pass of Observe calls. Only
// WithText of opts has an effect.
func (w *StateWatcher) Retain(keep map[string][]string, opts ...StateOption) error {
	var cfg observeConfig
	for _, o := range opts {
		o(&cfg)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	changed := false
	for roomID, keys := range w.data.Rooms {
		kept, has := keep[roomID]
		room, ok := w.rooms[roomID]
		if !ok {
			room = Room{ID: roomID}
		}
		for key, e := range keys {
			if has && slices.Contains(kept, key) {
				continue
			}
			for _, id := range slices.Clone(e.Breached) {
				t, ok := parseThreshold(id)
				if !ok {
					continue
				}
				e.Seq++
				c := StateChange{Key: key, Old: e.Value, New: []byte("null"), Threshold: &t}
				if err := w.emit.Event(w.event(room, EventStateRecovered, c, e.Seq, cfg.text)); err != nil {
					e.Seq--
					return errors.Join(err, w.saveIf(changed))
				}
				e.Breached = slices.DeleteFunc(e.Breached, func(s string) bool { return s == id })
				changed = true
			}
			delete(keys, key)
			changed = true
		}
		if len(keys) == 0 {
			delete(w.data.Rooms, roomID)
			delete(w.rooms, roomID)
		}
	}
	return w.saveIf(changed)
}

func (w *StateWatcher) saveIf(changed bool) error {
	if !changed {
		return nil
	}
	return w.save()
}

// event builds one event. The id comes from the epoch, the room, the key,
// and the sequence, so an emit again after a crash has the same id.
func (w *StateWatcher) event(room Room, typ string, c StateChange, seq int, text func(string, StateChange) string) Event {
	h := sha256.Sum256([]byte(w.data.Epoch + "\x00" + room.ID + "\x00" + c.Key + "\x00" + strconv.Itoa(seq)))
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

// save writes the store.
func (w *StateWatcher) save() error {
	if w.path == "" {
		return nil
	}
	b, err := json.Marshal(w.data)
	if err != nil {
		return err
	}
	return WriteFileAtomic(w.path, b, 0o600)
}
