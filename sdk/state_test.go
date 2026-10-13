package sdk

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type recordEmitter struct {
	events []Event
	err    error
}

func (r *recordEmitter) Event(e Event) error {
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, e)
	return nil
}

type observe struct {
	value any
	opts  []StateOption
	want  []string // event types, in order
}

func TestStateWatcherObserve(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		steps []observe
	}{
		{"first value emits nothing", []observe{{value: "a"}}},
		{"no change no event", []observe{{value: "a"}, {value: "a"}}},
		{"change one event", []observe{{value: "a"}, {value: "b", want: []string{EventStateChanged}}, {value: "b"}}},
		{"map compares by value", []observe{
			{value: map[string]int{"x": 1, "y": 2}},
			{value: map[string]int{"y": 2, "x": 1}},
		}},
		{"above crosses up once and back", []observe{
			{value: 50, opts: []StateOption{Above(90)}},
			{value: 80, opts: []StateOption{Above(90)}},
			{value: 95, opts: []StateOption{Above(90)}, want: []string{EventStateChanged}},
			{value: 99, opts: []StateOption{Above(90)}},
			{value: 90, opts: []StateOption{Above(90)}, want: []string{EventStateRecovered}},
			{value: 91, opts: []StateOption{Above(90)}, want: []string{EventStateChanged}},
		}},
		{"below crosses down and back", []observe{
			{value: 10, opts: []StateOption{Below(5)}},
			{value: 4, opts: []StateOption{Below(5)}, want: []string{EventStateChanged}},
			{value: 3, opts: []StateOption{Below(5)}},
			{value: 6, opts: []StateOption{Below(5)}, want: []string{EventStateRecovered}},
		}},
		{"first value past threshold emits", []observe{
			{value: 95, opts: []StateOption{Above(90)}, want: []string{EventStateChanged}},
		}},
		{"two thresholds at once", []observe{
			{value: 50, opts: []StateOption{Above(80), Above(90)}},
			{value: 95, opts: []StateOption{Above(80), Above(90)}, want: []string{EventStateChanged, EventStateChanged}},
			{value: 85, opts: []StateOption{Above(80), Above(90)}, want: []string{EventStateRecovered}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &recordEmitter{}
			w, err := NewStateWatcher(t.TempDir(), rec)
			if err != nil {
				t.Fatal(err)
			}
			for i, st := range tc.steps {
				before := len(rec.events)
				if err := w.Observe(Room{ID: "r1"}, "k", st.value, st.opts...); err != nil {
					t.Fatalf("step %d: %v", i, err)
				}
				var got []string
				for _, e := range rec.events[before:] {
					got = append(got, e.Type)
				}
				if !slices.Equal(got, st.want) {
					t.Fatalf("step %d (%v): events %v, want %v", i, st.value, got, st.want)
				}
			}
			seen := map[string]bool{}
			for _, e := range rec.events {
				if seen[e.ID] {
					t.Fatalf("event id %s twice", e.ID)
				}
				seen[e.ID] = true
			}
		})
	}
}

func TestStateWatcherEventShape(t *testing.T) {
	t.Parallel()
	rec := &recordEmitter{}
	w, err := NewStateWatcher("", rec)
	if err != nil {
		t.Fatal(err)
	}
	text := WithText(func(typ string, c StateChange) string { return typ + " " + c.Key + " " + string(c.New) })
	room := Room{ID: "host/a", Name: "a"}
	if err := w.Observe(room, "cpu", 50, Above(90), text); err != nil {
		t.Fatal(err)
	}
	if err := w.Observe(room, "cpu", 95.5, Above(90), text); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("events %d, want 1", len(rec.events))
	}
	e := rec.events[0]
	if e.Room == nil || e.Room.ID != "host/a" || e.TS == "" || e.Text != "state.changed cpu 95.5" {
		t.Fatalf("event %+v", e)
	}
	var c StateChange
	if err := json.Unmarshal(e.Data, &c); err != nil {
		t.Fatal(err)
	}
	if c.Key != "cpu" || string(c.Old) != "50" || string(c.New) != "95.5" || c.Threshold == nil || *c.Threshold != (Threshold{Op: ThresholdAbove, Value: 90}) {
		t.Fatalf("data %s", e.Data)
	}

	rec.events = nil
	if err := w.Observe(room, "title", "x"); err != nil {
		t.Fatal(err)
	}
	if err := w.Observe(room, "title", "y"); err != nil {
		t.Fatal(err)
	}
	if got := string(rec.events[0].Data); got != `{"key":"title","old":"x","new":"y"}` {
		t.Fatalf("data %s", got)
	}
}

func TestStateWatcherRestartKeepsValues(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rec := &recordEmitter{}
	w, err := NewStateWatcher(dir, rec)
	if err != nil {
		t.Fatal(err)
	}
	room := Room{ID: "r1"}
	for _, v := range []any{"a", "b"} {
		if err := w.Observe(room, "page", v); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Observe(room, "cpu", 95, Above(90)); err != nil {
		t.Fatal(err)
	}
	first := rec.events

	rec = &recordEmitter{}
	w, err = NewStateWatcher(dir, rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Observe(room, "page", "b"); err != nil {
		t.Fatal(err)
	}
	if err := w.Observe(room, "cpu", 96, Above(90)); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 0 {
		t.Fatalf("restart emitted %+v", rec.events)
	}
	if err := w.Observe(room, "page", "c"); err != nil {
		t.Fatal(err)
	}
	if err := w.Observe(room, "cpu", 10, Above(90)); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 2 || rec.events[1].Type != EventStateRecovered {
		t.Fatalf("events %+v", rec.events)
	}
	for _, e := range rec.events {
		for _, f := range first {
			if e.ID == f.ID {
				t.Fatalf("id %s used again after restart", e.ID)
			}
		}
	}
}

func TestStateWatcherFailedEmitKeepsOldValue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rec := &recordEmitter{}
	w, err := NewStateWatcher(dir, rec)
	if err != nil {
		t.Fatal(err)
	}
	room := Room{ID: "r1"}
	if err := w.Observe(room, "k", 1); err != nil {
		t.Fatal(err)
	}
	rec.err = errors.New("closed")
	if err := w.Observe(room, "k", 2); err == nil {
		t.Fatal("want the emit error")
	}
	rec.err = nil
	if err := w.Observe(room, "k", 2); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 || string(rec.events[0].Data) != `{"key":"k","old":1,"new":2}` {
		t.Fatalf("events %+v", rec.events)
	}
}

func TestStateWatcherInvalid(t *testing.T) {
	t.Parallel()
	w, err := NewStateWatcher("", &recordEmitter{})
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"no room":        func() error { return w.Observe(Room{}, "k", 1) },
		"no key":         func() error { return w.Observe(Room{ID: "r"}, "", 1) },
		"not a number":   func() error { return w.Observe(Room{ID: "r"}, "k", "high", Above(1)) },
		"not json-ready": func() error { return w.Observe(Room{ID: "r"}, "k", func() {}) },
	} {
		var e *Error
		if err := call(); !errors.As(err, &e) || e.Code != CodeInvalid {
			t.Errorf("%s: %v, want invalid", name, err)
		}
	}
}

func TestStateWatcherNewerFormat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, StateFile), []byte(`{"version":99,"rooms":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStateWatcher(dir, &recordEmitter{}); err == nil {
		t.Fatal("want an error for a newer format")
	}
}
