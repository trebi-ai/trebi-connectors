package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// classifiedEvent is a WhatsApp event after classification, ready to be filtered
// and rendered independently for each listener.
type classifiedEvent struct {
	evt       interface{}
	name      string
	category  string
	chatJID   string
	senderJID string
	isFromMe  bool
	payload   any
}

// render applies a single filter and, if the event matches, returns its JSONL
// line. The bool is false when the event is filtered out.
func (ce classifiedEvent) render(f ListenFilter) ([]byte, bool) {
	if !f.Categories[ce.category] {
		return nil, false
	}
	if f.ChatFilter != "" && ce.chatJID != "" && ce.chatJID != f.ChatFilter {
		return nil, false
	}
	if f.FromFilter != "" && ce.senderJID != "" && ce.senderJID != f.FromFilter {
		return nil, false
	}
	if f.ExcludeSelf && ce.isFromMe {
		return nil, false
	}

	var data any = ce.payload
	if f.Raw {
		data = ce.evt
	}
	line, err := json.Marshal(map[string]any{
		"t":  ce.name,
		"ts": time.Now().UTC().Format(time.RFC3339Nano),
		"d":  data,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nmarshal event %s: %v\n", ce.name, err)
		return nil, false
	}
	return line, true
}

// subscriber is one secondary listener attached over the socket.
type subscriber struct {
	filter ListenFilter
	// lines carries rendered JSONL lines to the subscriber's writer goroutine.
	lines chan []byte
	// done is closed when the subscriber is being torn down (slow/broken conn or
	// anchor shutdown) so dispatch stops trying to enqueue.
	done chan struct{}
}

// SubscriberSet is the anchor's registry of secondary listeners. It is safe for
// concurrent use: the WhatsApp event handler calls dispatch while subscribe/
// remove run from socket-handler goroutines.
type SubscriberSet struct {
	mu   sync.RWMutex
	subs map[uint64]*subscriber
	next uint64
}

// NewSubscriberSet creates an empty registry.
func NewSubscriberSet() *SubscriberSet {
	return &SubscriberSet{subs: make(map[uint64]*subscriber)}
}

// subscriberBuffer bounds how many pending lines a slow subscriber may hold
// before its connection is considered stuck and dropped.
const subscriberBuffer = 256

// add registers a subscriber and returns its id and the line channel the caller
// pumps to the socket. remove(id) must be called when the connection ends.
func (s *SubscriberSet) add(f ListenFilter) (uint64, *subscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.next
	s.next++
	sub := &subscriber{
		filter: f,
		lines:  make(chan []byte, subscriberBuffer),
		done:   make(chan struct{}),
	}
	s.subs[id] = sub
	return id, sub
}

// remove deregisters a subscriber and signals its teardown.
func (s *SubscriberSet) remove(id uint64) {
	s.mu.Lock()
	sub, ok := s.subs[id]
	if ok {
		delete(s.subs, id)
	}
	s.mu.Unlock()
	if ok {
		close(sub.done)
	}
}

// dispatch renders the event per-subscriber and enqueues matching lines. A
// subscriber whose buffer is full (a stalled reader) is skipped for this event
// rather than blocking the whole event handler.
func (s *SubscriberSet) dispatch(ce classifiedEvent) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sub := range s.subs {
		line, ok := ce.render(sub.filter)
		if !ok {
			continue
		}
		select {
		case sub.lines <- line:
		case <-sub.done:
		default:
			// Buffer full: drop this line for this slow subscriber. Its own
			// writer goroutine will surface the backpressure; we never block
			// the shared WhatsApp event handler.
		}
	}
}

// Count returns the number of currently attached subscribers.
func (s *SubscriberSet) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.subs)
}

// Serve registers a subscriber with filter f and writes its matching JSONL lines
// to w (one per line) until ctx is cancelled (anchor shutting down) or a write
// fails (subscriber disconnected). It always deregisters the subscriber before
// returning. flush, if non-nil, is called after each line so buffered writers
// deliver promptly.
func (s *SubscriberSet) Serve(ctx context.Context, f ListenFilter, w io.Writer, flush func() error) error {
	id, sub := s.add(f)
	defer s.remove(id)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line := <-sub.lines:
			if _, err := w.Write(append(line, '\n')); err != nil {
				return err
			}
			if flush != nil {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	}
}
