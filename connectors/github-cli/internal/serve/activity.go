package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// activityFile keeps the position of the notifications read.
const activityFile = "github-activity.json"

// maxThreads bounds the threads that the position keeps.
const maxThreads = 500

// MsgNoActivity is the status message for a login that cannot read the
// notifications.
const MsgNoActivity = "Your activity needs the GitHub login, not a fine-grained token."

// activityTypes are the event types of the notifications. activity takes
// each reason that has no type of its own.
var activityTypes = []string{"review_requested", "mention", "assigned", "ci_activity", "activity"} //nolint:gochecknoglobals // fixed list, never changed

// reasonTypes maps a notification reason to its event type.
var reasonTypes = map[string]string{ //nolint:gochecknoglobals // fixed map, never changed
	"review_requested": "review_requested",
	"mention":          "mention",
	"team_mention":     "mention",
	"assign":           "assigned",
	"ci_activity":      "ci_activity",
}

// account is the part of the adapter that the reader uses.
type account interface {
	gh() *client.Client
	identity() *client.Identity
	tokenLost(ctx context.Context, e sdk.Emitter, failed string) bool
}

// activityCursor is the content of activityFile. Threads maps a thread id
// to the updated_at of its last event; Order keeps the age of the ids.
type activityCursor struct {
	LastModified string            `json:"last_modified,omitempty"`
	Primed       bool              `json:"primed"`
	Threads      map[string]string `json:"threads"`
	Order        []string          `json:"order"`
}

// ActivityReader sends the "your activity" events: it reads the
// notifications of the account with If-Modified-Since. It never marks a
// notification as read.
type ActivityReader struct {
	acct account
	path string
	log  *slog.Logger

	mu      sync.Mutex
	cur     activityCursor
	loaded  bool
	stopped bool // the login cannot read the notifications
}

// NewActivityReader keeps the position in stateDir; empty keeps it in
// memory.
func NewActivityReader(acct account, stateDir string) *ActivityReader {
	r := &ActivityReader{acct: acct, log: slog.Default().With("source", "github_activity"), cur: activityCursor{Threads: map[string]string{}}}
	if stateDir != "" {
		r.path = filepath.Join(stateDir, activityFile)
	}
	return r
}

// Reset forgets the position after a new login, so the first read sends
// no event for old notifications.
func (r *ActivityReader) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cur, r.loaded, r.stopped = activityCursor{Threads: map[string]string{}}, true, false
	if err := r.save(); err != nil {
		r.log.Warn("save activity", "err", err)
	}
}

// Read reads the notifications once and sends an event for each new or
// updated thread. It returns the X-Poll-Interval that GitHub asked for.
func (r *ActivityReader) Read(ctx context.Context, e sdk.Emitter) (next time.Duration) {
	gh := r.acct.gh()
	r.mu.Lock()
	stopped := r.stopped
	if !r.loaded {
		r.load()
	}
	since := r.cur.LastModified
	r.mu.Unlock()
	if stopped || gh.Token == "" {
		return 0
	}
	page, err := gh.Notifications(ctx, since)
	switch client.StatusOf(err) {
	case 0:
	case http.StatusUnauthorized:
		r.acct.tokenLost(ctx, e, gh.Token)
		return 0
	case http.StatusForbidden, http.StatusNotFound:
		r.mu.Lock()
		r.stopped = true
		r.mu.Unlock()
		r.log.Warn("the login cannot read the notifications; the activity events stop until the next login", "err", err)
		st := sdk.Status{State: sdk.StateConnected, Message: MsgNoActivity}
		if id := r.acct.identity(); id != nil {
			st.Account = &sdk.Account{ID: id.Login, Name: id.Login}
			if id.Name != "" {
				st.Account.Name = id.Name
			}
		}
		_ = e.Status(st) //nolint:errcheck // the message is a hint only
		return 0
	}
	if err != nil {
		r.log.Warn("read notifications", "err", err)
		return 0
	}
	if page.NotModified {
		return page.Interval
	}
	items := slices.Clone(page.Items)
	slices.SortStableFunc(items, func(x, y client.Notification) int { return strings.Compare(x.UpdatedAt, y.UpdatedAt) })
	r.mu.Lock()
	primed := r.cur.Primed
	var fresh []client.Notification
	for _, n := range items {
		if r.cur.Threads[n.ID] == n.UpdatedAt {
			continue
		}
		if _, ok := r.cur.Threads[n.ID]; !ok {
			r.cur.Order = append(r.cur.Order, n.ID)
		}
		r.cur.Threads[n.ID] = n.UpdatedAt
		fresh = append(fresh, n)
	}
	for len(r.cur.Order) > maxThreads {
		delete(r.cur.Threads, r.cur.Order[0])
		r.cur.Order = r.cur.Order[1:]
	}
	r.cur.LastModified, r.cur.Primed = page.LastModified, true
	if err := r.save(); err != nil {
		r.log.Warn("save activity", "err", err)
	}
	r.mu.Unlock()
	if !primed {
		return page.Interval // the first read only finds the position
	}
	self := selfOf(r.acct.identity())
	for _, n := range fresh {
		if err := e.Event(r.toEvent(ctx, gh, n, self)); err != nil {
			r.log.Warn("emit", "err", err)
		}
	}
	return page.Interval
}

// subject is the part of an issue, a pull request, a release, or a comment
// that the event uses.
type subject struct {
	Number  int         `json:"number"`
	Title   string      `json:"title"`
	HTMLURL string      `json:"html_url"`
	User    client.User `json:"user"`
	Author  client.User `json:"author"` // a release
}

func (s subject) by() client.User {
	if s.User.Login != "" {
		return s.User
	}
	return s.Author
}

// toEvent builds the event of one thread. It reads the subject and the
// newest comment for the URL and the author.
func (r *ActivityReader) toEvent(ctx context.Context, gh *client.Client, n client.Notification, self *client.User) sdk.Event {
	repo := n.Repository.FullName
	typ := reasonTypes[n.Reason]
	if typ == "" {
		typ = "activity"
	}
	var subj, latest subject
	if n.Subject.URL != "" {
		if err := gh.Do(ctx, http.MethodGet, n.Subject.URL, nil, &subj); err != nil {
			r.log.Debug("read the subject", "url", n.Subject.URL, "err", err)
		}
	}
	sender := subj.by()
	if u := n.Subject.LatestCommentURL; u != "" && u != n.Subject.URL {
		if err := gh.Do(ctx, http.MethodGet, u, nil, &latest); err == nil && latest.by().Login != "" {
			sender = latest.by()
		}
	}
	number := subj.Number
	if number == 0 {
		number = numberOf(n.Subject.URL)
	}
	url := subj.HTMLURL
	if latest.HTMLURL != "" && typ != "review_requested" && typ != "assigned" {
		url = latest.HTMLURL
	}
	if url == "" {
		url = "https://github.com/" + repo
	}
	title := n.Subject.Title
	ev := sdk.Event{
		ID: "notif:" + n.ID + ":" + n.UpdatedAt, Type: typ, TS: n.UpdatedAt,
		Room: &sdk.Room{ID: repo, Name: repo, Kind: sdk.RoomChannel},
	}
	if sender.Login != "" {
		ev.Sender = &sdk.Author{ID: sender.Login, Name: sender.Login, Bot: sender.Type == "Bot", Self: self != nil && sender.Login == self.Login}
	}
	data := map[string]any{
		"reason": n.Reason, "subject_type": n.Subject.Type, "title": title, "url": url,
		"repository": repo, "thread_id": n.ID, "updated_at": n.UpdatedAt,
	}
	ref := repo
	if number > 0 {
		data["number"] = number
		ref = fmt.Sprintf("%s#%d", repo, number)
		ev.Thread = &sdk.Thread{ID: strconv.Itoa(number), Title: title}
	}
	who := sender.Login
	if who == "" {
		who = "Someone"
	}
	switch {
	case typ == "review_requested":
		ev.Text = fmt.Sprintf("%s asked for your review on %s: %s", who, ref, title)
	case n.Reason == "team_mention":
		ev.Text = fmt.Sprintf("%s mentioned your team on %s: %s", who, ref, title)
	case typ == "mention":
		ev.Text = fmt.Sprintf("%s mentioned you on %s: %s", who, ref, title)
	case typ == "assigned":
		ev.Text = fmt.Sprintf("%s assigned %s to you: %s", who, ref, title)
	case typ == "ci_activity":
		ev.Text = fmt.Sprintf("%s on %s", title, repo)
	default:
		ev.Text = fmt.Sprintf("New activity (%s) on %s: %s", n.Reason, ref, title)
	}
	ev.Data, _ = json.Marshal(data) //nolint:errcheck // a map of plain values
	raw, _ := json.Marshal(n)       //nolint:errcheck // a struct of plain values
	ev.Raw = raw
	return ev
}

// numberOf returns the number at the end of an issue or pull request API
// URL, or 0.
func numberOf(url string) int {
	i := strings.LastIndex(url, "/")
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(url[i+1:])
	if err != nil || !strings.Contains(url, "/issues/") && !strings.Contains(url, "/pulls/") {
		return 0
	}
	return n
}

// load reads the position. The caller holds mu.
func (r *ActivityReader) load() {
	r.loaded = true
	if r.path == "" {
		return
	}
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var cur activityCursor
	if err == nil {
		err = json.Unmarshal(data, &cur)
	}
	if err != nil {
		r.log.Warn("read activity; the next read finds the position again", "err", err)
		return
	}
	if cur.Threads == nil {
		cur.Threads = map[string]string{}
	}
	r.cur = cur
}

// save writes the position. The caller holds mu.
func (r *ActivityReader) save() error {
	if r.path == "" {
		return nil
	}
	data, err := json.Marshal(r.cur)
	if err != nil {
		return err
	}
	return sdk.WriteFileAtomic(r.path, data, 0o600)
}
