// Package listen streams live Slack events.
package listen

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Source is one workspace event stream.
type Source interface {
	// Run sends events until ctx is done or the stream drops.
	Run(ctx context.Context, out chan<- Event) error
	// Name returns the workspace label.
	Name() string
}

// Stream runs a source and reconnects with backoff when it drops. It returns
// when ctx is done. onError, if set, reports each drop before the reconnect.
func Stream(ctx context.Context, s Source, out chan<- Event, onError func(string, error)) error {
	const (
		minBackoff = 1 * time.Second
		maxBackoff = 30 * time.Second
	)
	backoff := minBackoff
	for {
		start := time.Now()
		err := s.Run(ctx, out)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(start) > time.Minute {
			backoff = minBackoff // a long run is healthy; reset
		}
		if onError != nil {
			if err == nil {
				err = fmt.Errorf("stream closed")
			}
			onError(s.Name(), err)
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// Event is one normalized event from a workspace stream.
type Event struct {
	Workspace string          `json:"workspace"`
	TeamID    string          `json:"team_id,omitempty"`
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype,omitempty"`
	Channel   string          `json:"channel,omitempty"`
	User      string          `json:"user,omitempty"`
	TS        string          `json:"ts,omitempty"`
	ThreadTS  string          `json:"thread_ts,omitempty"`
	Text      string          `json:"text,omitempty"`
	Time      time.Time       `json:"time"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

// parseEvent builds an Event from a raw Slack event object. ok is false for
// frames that are not events (hello, pong, acks without a payload).
func parseEvent(workspace, teamID string, raw json.RawMessage) (Event, bool) {
	var e struct {
		Type     string          `json:"type"`
		Subtype  string          `json:"subtype"`
		Channel  json.RawMessage `json:"channel"`
		User     string          `json:"user"`
		TS       string          `json:"ts"`
		EventTS  string          `json:"event_ts"`
		ThreadTS string          `json:"thread_ts"`
		Text     string          `json:"text"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Type == "" || e.Type == "hello" || e.Type == "pong" {
		return Event{}, false
	}
	ev := Event{
		Workspace: workspace, TeamID: teamID, Type: e.Type, Subtype: e.Subtype,
		Channel: channelID(e.Channel), User: e.User, TS: firstNonEmpty(e.TS, e.EventTS),
		ThreadTS: e.ThreadTS, Text: e.Text, Time: time.Now(), Raw: raw,
	}
	return ev, true
}

// channelID reads a channel field that is a string id or an object {id,...}.
func channelID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &obj)
	return obj.ID
}

// Filter decides which events to emit.
type Filter struct {
	Channels map[string]bool // channel ids
	Users    map[string]bool // user ids
	Types    map[string]bool // event types
	Thread   string          // only this thread ts
	SelfID   string          // the token owner's user id
	Mentions bool            // only messages that mention SelfID
	DMs      bool            // only direct-message channels
	NoSelf   bool            // drop events authored by SelfID
}

// Match reports whether e passes the filter.
func (f Filter) Match(e Event) bool {
	if len(f.Types) > 0 && !f.Types[e.Type] {
		return false
	}
	if len(f.Channels) > 0 && !f.Channels[e.Channel] {
		return false
	}
	if len(f.Users) > 0 && !f.Users[e.User] {
		return false
	}
	if f.Thread != "" && e.ThreadTS != f.Thread && e.TS != f.Thread {
		return false
	}
	if f.NoSelf && f.SelfID != "" && e.User == f.SelfID {
		return false
	}
	if f.DMs && !strings.HasPrefix(e.Channel, "D") {
		return false
	}
	if f.Mentions && f.SelfID != "" && !strings.Contains(e.Text, "<@"+f.SelfID+">") {
		return false
	}
	return true
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
