// Package watch sends one event for each new message in a folder. IDLE is
// only a wake-up signal. A catch-up sync of the UIDs after the cursor is
// the source of truth: it runs after each EXISTS, after each reconnect,
// and on a timer.
package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/mail"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// EventType is the type of the event for a new message.
const EventType = "mail.received"

// Defaults of the loop.
const (
	syncEvery    = 5 * time.Minute
	clockTick    = 15 * time.Second
	clockJump    = time.Minute
	noopTimeout  = 2 * time.Minute
	minBackoff   = time.Second
	maxBackoff   = 2 * time.Minute
	failsToError = 10
)

var (
	errClosed = errors.New("the server closed the connection")
	errJump   = errors.New("the clock jumped (sleep and wake)")
)

// Watcher watches one folder of one account.
type Watcher struct {
	mb     *mail.Mailbox
	store  CursorStore
	now    func() time.Time
	folder string
	every  time.Duration
	tick   time.Duration
	min    time.Duration
	max    time.Duration
	report func(sdk.Status)
	log    *slog.Logger
}

// Option changes a Watcher.
type Option func(*Watcher)

// WithFolder replaces INBOX.
func WithFolder(f string) Option { return func(w *Watcher) { w.folder = f } }

// WithSyncInterval replaces the 5 min timer of the catch-up sync.
func WithSyncInterval(d time.Duration) Option { return func(w *Watcher) { w.every = d } }

// WithClockTick replaces the 15 s interval of the clock jump check.
func WithClockTick(d time.Duration) Option { return func(w *Watcher) { w.tick = d } }

// WithBackoff replaces the reconnect backoff of 1 s to 2 min.
func WithBackoff(lo, hi time.Duration) Option { return func(w *Watcher) { w.min, w.max = lo, hi } }

// WithStatus receives each status of the connection.
func WithStatus(f func(sdk.Status)) Option { return func(w *Watcher) { w.report = f } }

// NewWatcher builds a Watcher. now is the wall clock; a jump of more than
// one minute between two ticks forces a reconnect.
func NewWatcher(mb *mail.Mailbox, store CursorStore, now func() time.Time, opts ...Option) *Watcher {
	w := &Watcher{
		mb: mb, store: store, now: now, folder: "INBOX",
		every: syncEvery, tick: clockTick, min: minBackoff, max: maxBackoff,
		report: func(sdk.Status) {}, log: slog.Default().With("source", "mailbox.watch"),
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Run watches until ctx ends. It reconnects after each fault.
func (w *Watcher) Run(ctx context.Context, emit func(sdk.Event) error) error {
	if _, _, err := w.store.Load(); errors.Is(err, ErrNewerState) {
		w.report(sdk.Status{State: sdk.StateError, Message: err.Error()})
		<-ctx.Done()
		return nil
	}
	acct := &sdk.Account{ID: w.mb.Address(), Name: w.mb.Address()}
	fails, backoff := 0, w.min
	for {
		conn, err := w.mb.Watch(ctx, w.folder)
		if ctx.Err() != nil {
			return nil
		}
		wait := backoff
		if err != nil {
			fails++
			switch {
			case errors.Is(err, mail.ErrAuth):
				w.report(sdk.Status{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked, Message: w.mb.AuthMessage(err)})
				wait = max(backoff, w.every)
			case fails >= failsToError:
				w.report(sdk.Status{State: sdk.StateError, Message: err.Error()})
			default:
				w.report(sdk.Status{State: sdk.StateConnecting, Message: err.Error()})
			}
			w.log.Warn("connect", "err", err, "fails", fails)
		} else {
			w.report(sdk.Status{State: sdk.StateConnected, Account: acct})
			start := w.now()
			err = w.serve(ctx, conn, emit)
			conn.Close()
			if ctx.Err() != nil {
				return nil
			}
			w.log.Warn("connection ended", "err", err)
			if w.now().Sub(start) > w.max || errors.Is(err, errJump) {
				fails, backoff, wait = 0, w.min, 0
			}
			w.report(sdk.Status{State: sdk.StateConnecting, Message: err.Error()})
		}
		if wait > 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(wait):
			}
		}
		backoff = min(2*backoff, w.max)
	}
}

// serve syncs, then waits in IDLE for news.
func (w *Watcher) serve(ctx context.Context, conn *mail.WatchConn, emit func(sdk.Event) error) error {
	if err := w.sync(conn, emit); err != nil {
		return err
	}
	timer := time.NewTimer(w.every)
	defer timer.Stop()
	tick := time.NewTicker(w.tick)
	defer tick.Stop()
	last := w.now().Round(0)
	for {
		stop, err := conn.Idle()
		if err != nil {
			return err
		}
		timed, err := w.wait(ctx, conn, timer.C, tick.C, &last)
		if err != nil || ctx.Err() != nil {
			_ = stop() //nolint:errcheck // the connection closes next
			return err
		}
		if err := stop(); err != nil {
			return fmt.Errorf("stop idle: %w", err)
		}
		if timed {
			if err := conn.Noop(noopTimeout); err != nil {
				return err
			}
			timer.Reset(w.every)
		}
		if err := w.sync(conn, emit); err != nil {
			return err
		}
	}
}

// wait blocks in IDLE until news, the timer, a clock jump, or the end.
// timed is true for the timer.
func (w *Watcher) wait(ctx context.Context, conn *mail.WatchConn, timer, tick <-chan time.Time, last *time.Time) (timed bool, err error) {
	for {
		select {
		case <-ctx.Done():
			return false, nil
		case <-conn.Closed():
			return false, errClosed
		case <-conn.Exists():
			return false, nil
		case <-timer:
			return true, nil
		case <-tick:
			n := w.now().Round(0) // the wall clock, not the monotonic one
			jumped := n.Sub(*last) > w.tick+clockJump
			*last = n
			if jumped {
				return false, errJump
			}
		}
	}
}

// sync sends an event for each message after the cursor and moves the
// cursor after each event. A new folder, or a folder that the server made
// again, gets a new baseline and no events.
func (w *Watcher) sync(conn *mail.WatchConn, emit func(sdk.Event) error) error {
	cur, ok, err := w.store.Load()
	if err != nil {
		return err
	}
	if !ok || cur.UIDValidity != conn.UIDValidity {
		if ok {
			w.log.Warn("the server reset the folder; mail before now sends no event", "folder", w.folder, "old", cur.UIDValidity, "new", conn.UIDValidity)
		}
		last, err := conn.LastUID()
		if err != nil {
			return err
		}
		cur = Cursor{UIDValidity: conn.UIDValidity, LastUID: uint32(last)}
		return w.store.Save(cur)
	}
	mails, err := conn.Fetch(imap.UID(cur.LastUID))
	if err != nil {
		return err
	}
	for _, nm := range mails {
		if !strings.EqualFold(nm.FromAddr, w.mb.Address()) {
			if err := emit(w.event(nm, conn.UIDValidity)); err != nil {
				return fmt.Errorf("send event: %w", err)
			}
		}
		cur.LastUID = uint32(nm.UID)
		if err := w.store.Save(cur); err != nil {
			return err
		}
	}
	return nil
}

// Data is the data of a mail.received event (schemas/mail.received.json).
type Data struct {
	ID             string   `json:"id"`
	From           string   `json:"from"`
	To             []string `json:"to"`
	Cc             []string `json:"cc"`
	Subject        string   `json:"subject"`
	Date           string   `json:"date,omitempty"`
	MessageID      string   `json:"message_id,omitempty"`
	ThreadID       string   `json:"thread_id,omitempty"`
	Labels         []string `json:"labels"`
	Snippet        string   `json:"snippet"`
	HasAttachments bool     `json:"has_attachments"`
	Size           int64    `json:"size"`
}

func (w *Watcher) event(nm mail.NewMail, uidValidity uint32) sdk.Event {
	id := fmt.Sprintf("%d:%d", uidValidity, nm.UID)
	if strings.HasPrefix(nm.ID, mail.GmailPrefix) {
		id = nm.ID
	}
	labels := nm.Labels
	if labels == nil {
		labels = []string{}
	}
	data, _ := json.Marshal(Data{ //nolint:errchkjson // plain fields
		ID: nm.ID, From: nm.From, To: nm.To, Cc: nm.Cc, Subject: nm.Subject, Date: nm.Date,
		MessageID: nm.MessageID, ThreadID: nm.ThreadID, Labels: labels, Snippet: nm.Snippet,
		HasAttachments: nm.HasAttachments, Size: nm.Size,
	})
	ts := nm.Date
	if ts == "" {
		ts = w.now().UTC().Format(time.RFC3339)
	}
	e := sdk.Event{
		ID: id, Type: EventType, TS: ts,
		Room:    &sdk.Room{ID: w.folder, Name: w.folder},
		Sender:  &sdk.Author{ID: nm.FromAddr, Name: nm.FromName},
		Text:    strings.TrimSpace(nm.Subject + "\n\n" + nm.Snippet),
		ReplyTo: nm.InReplyTo,
		Data:    data,
	}
	if nm.ThreadID != "" {
		e.Thread = &sdk.Thread{ID: nm.ThreadID, Title: nm.Subject}
	}
	for _, a := range nm.Attachments {
		e.Attachments = append(e.Attachments, sdk.Attachment{Name: a.Name, Mime: a.Type, Size: a.Size})
	}
	return e
}
