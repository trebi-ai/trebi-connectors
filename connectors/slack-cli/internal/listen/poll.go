package listen

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"

	"github.com/flarco/cli-tools/slack-cli/internal/client"
)

// PollSource polls conversations.history for channels. Use it when a socket
// is not available (OAuth bot/user tokens without Socket Mode). It reports
// new messages only.
type PollSource struct {
	Workspace string
	TeamID    string
	Client    *client.Client
	Channels  []string      // channel ids to poll
	Interval  time.Duration // between polls; 0 uses 5s
	cursors   map[string]string
}

// Run polls until ctx is done. The first poll sets the baseline and emits
// nothing; later polls emit messages after the last seen ts per channel.
func (s *PollSource) Run(ctx context.Context, out chan<- Event) error {
	interval := s.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	s.cursors = make(map[string]string)
	for _, ch := range s.Channels {
		s.cursors[ch] = ""
	}
	first := true
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		for _, ch := range s.Channels {
			if err := s.pollChannel(ctx, ch, first, out); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// Keep polling other channels on a transient error.
			}
		}
		first = false
		select {
		case <-tick.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *PollSource) pollChannel(ctx context.Context, ch string, first bool, out chan<- Event) error {
	q := url.Values{"channel": {ch}, "limit": {"100"}}
	if last := s.cursors[ch]; last != "" {
		q.Set("oldest", last)
	}
	body, err := s.Client.Call(ctx, "conversations.history", q)
	if err != nil {
		return err
	}
	page, _ := client.ParsePage(body)
	// Messages come newest first; walk oldest to newest.
	for i := len(page.Items) - 1; i >= 0; i-- {
		raw := page.Items[i]
		var m struct {
			TS string `json:"ts"`
		}
		if json.Unmarshal(raw, &m) != nil || m.TS == "" {
			continue
		}
		if m.TS == s.cursors[ch] {
			continue // "oldest" is inclusive; skip the baseline message
		}
		if newer(m.TS, s.cursors[ch]) {
			s.cursors[ch] = m.TS
		}
		if first {
			continue
		}
		if e, ok := parseEvent(s.Workspace, s.TeamID, raw); ok {
			if e.Channel == "" {
				e.Channel = ch
			}
			select {
			case out <- e:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}

// Name returns the workspace label.
func (s *PollSource) Name() string { return s.Workspace }

// newer reports whether ts a is after ts b. A Slack ts is "seconds.micros".
func newer(a, b string) bool {
	if b == "" {
		return true
	}
	fa, _ := strconv.ParseFloat(a, 64)
	fb, _ := strconv.ParseFloat(b, 64)
	return fa > fb
}
