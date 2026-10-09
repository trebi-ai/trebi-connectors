package serve

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// FieldList is the one form field.
const FieldList = "list"

// Options lists the To Do lists that match the query.
func (a *Adapter) Options(ctx context.Context, q sdk.OptionQuery) (sdk.OptionPage, error) {
	if q.Field != FieldList {
		return sdk.OptionPage{}, sdk.Invalid("unknown field " + q.Field)
	}
	lists, err := a.cl.Lists(ctx)
	if err != nil {
		return sdk.OptionPage{}, wireErr(err)
	}
	page := sdk.OptionPage{Options: []sdk.FieldOption{}}
	needle := strings.ToLower(q.Query)
	for _, l := range lists {
		if needle != "" && !strings.Contains(strings.ToLower(l.DisplayName), needle) {
			continue
		}
		page.Options = append(page.Options, sdk.FieldOption{Value: l.ID, Label: l.DisplayName})
		if q.Limit > 0 && len(page.Options) == q.Limit {
			break
		}
	}
	return page, nil
}

// Sync makes the Graph subscriptions match the list. With a webhook each
// list gets a Graph subscription; without one, the lists are polled.
func (a *Adapter) Sync(ctx context.Context, p sdk.SyncParams) (sdk.SyncResult, error) {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	a.mu.Lock()
	hook := a.hook
	a.mu.Unlock()
	graph := map[string]client.Subscription{}
	if hook != "" {
		subs, err := a.cl.Subscriptions(ctx)
		if err != nil {
			return sdk.SyncResult{}, wireErr(err)
		}
		for _, s := range subs {
			graph[s.ID] = s
		}
	}
	res := sdk.SyncResult{Subscriptions: make([]sdk.SubscriptionState, 0, len(p.Subscriptions))}
	keep := map[string]bool{}
	for _, s := range p.Subscriptions {
		st, err := a.syncOne(ctx, s, hook, graph)
		if errors.Is(err, client.ErrAuth) || errors.Is(err, client.ErrNoLogin) {
			return sdk.SyncResult{}, wireErr(err)
		}
		res.Subscriptions = append(res.Subscriptions, st)
		keep[s.ID] = true
	}
	a.mu.Lock()
	inUse := map[string]bool{}
	var gone []string
	for id, e := range a.st.Subs {
		switch {
		case keep[id]:
			inUse[e.GraphID] = true
		case e.GraphID != "":
			gone = append(gone, e.GraphID)
			delete(a.st.Subs, id)
		default:
			delete(a.st.Subs, id)
		}
	}
	err := a.saveLocked()
	a.mu.Unlock()
	if err != nil {
		return sdk.SyncResult{}, err
	}
	for id, g := range graph {
		if !inUse[id] && g.NotificationURL == hook {
			gone = append(gone, id)
		}
	}
	slices.Sort(gone)
	for _, id := range slices.Compact(gone) {
		if err := a.cl.DeleteSubscription(ctx, id); err != nil {
			a.log.Warn("mstodo.subscription.delete", "graph_id", id, "err", err)
		}
	}
	return res, nil
}

// syncOne serves one subscription. A platform error gives the state error;
// only a lost login returns an error.
func (a *Adapter) syncOne(ctx context.Context, s sdk.Subscription, hook string, graph map[string]client.Subscription) (sdk.SubscriptionState, error) {
	mode := sdk.ModePoll
	if hook != "" {
		mode = sdk.ModeAPI
	}
	fail := func(msg string) sdk.SubscriptionState {
		return sdk.SubscriptionState{ID: s.ID, Mode: mode, State: sdk.SubscriptionError, Message: msg}
	}
	listID := s.Value(FieldList)
	if listID == "" {
		return fail("Choose a list."), nil
	}
	l, err := a.cl.List(ctx, listID)
	switch {
	case client.IsNotFound(err):
		return fail("This list is not in Microsoft To Do."), nil
	case errors.Is(err, client.ErrAuth), errors.Is(err, client.ErrNoLogin):
		return sdk.SubscriptionState{}, err
	case err != nil:
		return fail(err.Error()), nil
	}
	if err := a.baseline(ctx, l.ID); err != nil {
		a.log.Warn("mstodo.delta", "list", l.ID, "err", err)
	}
	a.mu.Lock()
	var prior entry
	if e := a.st.Subs[s.ID]; e != nil {
		prior = *e
	}
	a.mu.Unlock()
	want := entry{Mode: mode, ListID: l.ID, ListName: l.DisplayName}
	if prior.GraphID != "" && hook != "" && prior.URL == hook && prior.ListID == l.ID {
		if g, ok := graph[prior.GraphID]; ok && g.NotificationURL == hook && sameResource(g.Resource, resource(l.ID)) {
			want = prior
			want.ListName = l.DisplayName
			want.ExpiresAt = g.ExpirationDateTime
			a.setEntry(s.ID, want)
			return want.state(s.ID), nil
		}
	}
	if prior.GraphID != "" {
		if err := a.cl.DeleteSubscription(ctx, prior.GraphID); err != nil {
			a.log.Warn("mstodo.subscription.delete", "graph_id", prior.GraphID, "err", err)
		}
	}
	if hook == "" {
		a.setEntry(s.ID, want)
		return want.state(s.ID), nil
	}
	e, err := a.create(ctx, s.ID, want, hook)
	switch {
	case errors.Is(err, client.ErrAuth), errors.Is(err, client.ErrNoLogin):
		return sdk.SubscriptionState{}, err
	case err != nil:
		return fail("Microsoft does not accept the subscription: " + err.Error()), nil
	}
	return e.state(s.ID), nil
}

func (e entry) state(id string) sdk.SubscriptionState {
	st := sdk.SubscriptionState{ID: id, Title: e.ListName, Room: e.room(), Mode: e.Mode, State: sdk.SubscriptionPolling}
	if e.Mode == sdk.ModeAPI {
		st.State = sdk.SubscriptionActive
		st.ExpiresAt = sdk.FormatTime(e.ExpiresAt)
	}
	return st
}

func (a *Adapter) setEntry(id string, e entry) {
	a.mu.Lock()
	a.st.Subs[id] = &e
	a.mu.Unlock()
}

func resource(list string) string { return "/me/todo/lists/" + list + "/tasks" }

// sameResource compares two Graph resources; Graph can drop the slash and
// change the case.
func sameResource(a, b string) bool {
	return strings.EqualFold(strings.TrimPrefix(a, "/"), strings.TrimPrefix(b, "/"))
}

// create makes a Graph subscription for one entry. It saves the
// clientState first, so that a notification during the create is known.
// The caller holds syncMu.
func (a *Adapter) create(ctx context.Context, id string, e entry, hook string) (entry, error) {
	e.Mode, e.URL, e.ClientState, e.GraphID = sdk.ModeAPI, hook, client.Secret(32), ""
	a.mu.Lock()
	a.st.Subs[id] = &e
	err := a.saveLocked()
	a.mu.Unlock()
	if err != nil {
		return e, err
	}
	now := a.now()
	g, err := a.cl.CreateSubscription(ctx, client.Subscription{
		ChangeType:               "created,updated,deleted",
		NotificationURL:          hook,
		LifecycleNotificationURL: hook + "/lifecycle",
		Resource:                 resource(e.ListID),
		ExpirationDateTime:       now.Add(lifetime).UTC(),
		ClientState:              e.ClientState,
	})
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		delete(a.st.Subs, id)
		return e, errors.Join(err, a.saveLocked())
	}
	e.GraphID, e.Since, e.ExpiresAt = g.ID, now, g.ExpirationDateTime
	if e.ExpiresAt.IsZero() {
		e.ExpiresAt = now.Add(lifetime)
	}
	a.st.Subs[id] = &e
	return e, a.saveLocked()
}

// renewDue renews each Graph subscription that has used 75% of its time.
func (a *Adapter) renewDue(ctx context.Context, emit sdk.Emitter) error {
	now := a.now()
	var due []string
	a.mu.Lock()
	for id, e := range a.st.Subs {
		if e.GraphID != "" && !now.Before(e.Since.Add(e.ExpiresAt.Sub(e.Since)*3/4)) {
			due = append(due, id)
		}
	}
	a.mu.Unlock()
	var errs []error
	for _, id := range due {
		errs = append(errs, a.renew(ctx, id, emit))
	}
	return errors.Join(errs...)
}

// renew moves the expiry of one Graph subscription. A subscription that
// Graph dropped is created again.
func (a *Adapter) renew(ctx context.Context, id string, emit sdk.Emitter) error {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	a.mu.Lock()
	e, ok := a.st.Subs[id]
	var cur entry
	if ok {
		cur = *e
	}
	hook := a.hook
	a.mu.Unlock()
	if !ok || cur.GraphID == "" {
		return nil
	}
	now := a.now()
	g, err := a.cl.RenewSubscription(ctx, cur.GraphID, now.Add(lifetime).UTC())
	switch {
	case client.IsNotFound(err) && hook != "":
		if _, err := a.create(ctx, id, cur, hook); err != nil {
			return fmt.Errorf("create the subscription again: %w", err)
		}
	case err != nil:
		return fmt.Errorf("renew subscription %s: %w", cur.GraphID, err)
	default:
		cur.Since, cur.ExpiresAt = now, g.ExpirationDateTime
		if cur.ExpiresAt.IsZero() {
			cur.ExpiresAt = now.Add(lifetime)
		}
		a.mu.Lock()
		a.st.Subs[id] = &cur
		err = a.saveLocked()
		a.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if emit != nil {
		return emit.SubscriptionsChanged()
	}
	return nil
}

// notification is one item of a Graph delivery: a change or a lifecycle
// event.
type notification struct {
	SubscriptionID                 string `json:"subscriptionId"`
	SubscriptionExpirationDateTime string `json:"subscriptionExpirationDateTime"`
	ChangeType                     string `json:"changeType"`
	Resource                       string `json:"resource"`
	ResourceData                   struct {
		ID string `json:"id"`
	} `json:"resourceData"`
	LifecycleEvent string `json:"lifecycleEvent"`
	ClientState    string `json:"clientState"`
}

// ReceiveWebhook checks each clientState, then emits one task event for
// each change. A lifecycle delivery renews, creates again, or runs a delta.
func (a *Adapter) ReceiveWebhook(ctx context.Context, req sdk.WebhookRequest) error {
	q, _ := url.ParseQuery(req.Query) //nolint:errcheck // a bad query has no validationToken
	if req.Handshake || q.Get("validationToken") != "" {
		return nil
	}
	var body struct {
		Value []notification `json:"value"`
	}
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil || len(body.Value) == 0 {
		return sdk.Invalid("the body is not a Graph notification")
	}
	ids := make([]string, len(body.Value))
	entries := make([]entry, len(body.Value))
	for i, n := range body.Value {
		id, e, err := a.match(n)
		if err != nil {
			return err
		}
		ids[i], entries[i] = id, e
	}
	emit := sdk.EmitterFrom(ctx)
	lifecycle := strings.HasSuffix(req.Path, "/lifecycle")
	var errs []error
	for i, n := range body.Value {
		if lifecycle || n.LifecycleEvent != "" {
			errs = append(errs, a.lifecycle(ctx, ids[i], entries[i], n.LifecycleEvent, emit))
			continue
		}
		ev, ok, err := a.changeEvent(ctx, n, entries[i], req.ReceivedAt)
		if err != nil {
			errs = append(errs, wireErr(err))
			continue
		}
		if ok && emit != nil {
			errs = append(errs, emit.Event(ev))
		}
	}
	return errors.Join(errs...)
}

// match finds the entry of a notification and checks its clientState in
// constant time.
func (a *Adapter) match(n notification) (string, entry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	same := func(e *entry) bool {
		return e.ClientState != "" && subtle.ConstantTimeCompare([]byte(n.ClientState), []byte(e.ClientState)) == 1
	}
	for id, e := range a.st.Subs {
		if e.GraphID != "" && e.GraphID == n.SubscriptionID {
			if !same(e) {
				return "", entry{}, sdk.Invalid("the clientState does not match")
			}
			return id, *e, nil
		}
	}
	for _, e := range a.st.Subs {
		if e.GraphID == "" && same(e) {
			return "", entry{}, sdk.Transient("the subscription is still in creation")
		}
	}
	return "", entry{}, sdk.Invalid("unknown subscription " + n.SubscriptionID)
}

// lifecycle handles one lifecycle event of a Graph subscription.
func (a *Adapter) lifecycle(ctx context.Context, id string, e entry, event string, emit sdk.Emitter) error {
	switch event {
	case "reauthorizationRequired":
		return a.renew(ctx, id, emit)
	case "subscriptionRemoved":
		a.mu.Lock()
		hook := a.hook
		a.mu.Unlock()
		if hook == "" {
			return nil
		}
		a.syncMu.Lock()
		_, err := a.create(ctx, id, e, hook)
		a.syncMu.Unlock()
		if err != nil {
			return wireErr(fmt.Errorf("create the subscription again: %w", err))
		}
		if emit != nil {
			return emit.SubscriptionsChanged()
		}
		return nil
	case "missed":
		return wireErr(a.pollList(ctx, e, emit))
	}
	return nil
}

// changeEvent fetches the task of one change. A deleted task has no fetch.
// A task that is gone before the fetch gives no event.
func (a *Adapter) changeEvent(ctx context.Context, n notification, e entry, receivedAt string) (sdk.Event, bool, error) {
	ts := a.now()
	if t, err := time.Parse(time.RFC3339Nano, receivedAt); err == nil {
		ts = t
	}
	taskID := n.ResourceData.ID
	if taskID == "" {
		taskID = n.Resource[strings.LastIndex(n.Resource, "/")+1:]
	}
	if n.ChangeType == "deleted" {
		last := n.SubscriptionExpirationDateTime
		if last == "" {
			last = receivedAt
		}
		return deletedEvent(fmt.Sprintf("%s:%s:%s:%s", n.SubscriptionID, n.Resource, n.ChangeType, last), taskID, e, ts), true, nil
	}
	t, err := a.cl.Task(ctx, e.ListID, taskID)
	if client.IsNotFound(err) {
		return sdk.Event{}, false, nil
	}
	if err != nil {
		return sdk.Event{}, false, err
	}
	return taskEvent(fmt.Sprintf("%s:%s:%s:%s", n.SubscriptionID, n.Resource, n.ChangeType, t.LastModifiedDateTime), n.ChangeType, t, e, ts), true, nil
}

// taskData is the data of a task event. catalog/mstodo/schemas/task.json
// describes it.
type taskData struct {
	Change      string `json:"change"`
	TaskID      string `json:"task_id"`
	ListID      string `json:"list_id"`
	Title       string `json:"title,omitempty"`
	Status      string `json:"status,omitempty"`
	Importance  string `json:"importance,omitempty"`
	Note        string `json:"note,omitempty"`
	Due         string `json:"due,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	ModifiedAt  string `json:"modified_at,omitempty"`
}

func taskEvent(id, change string, t client.Task, e entry, fallback time.Time) sdk.Event {
	d := taskData{
		Change: change, TaskID: t.ID, ListID: e.ListID, Title: t.Title, Status: t.Status,
		Importance: t.Importance, ModifiedAt: t.LastModifiedDateTime,
	}
	if t.Body != nil {
		d.Note = t.Body.Content
	}
	if t.DueDateTime != nil {
		d.Due = t.DueDateTime.DateTime
	}
	if t.CompletedDateTime != nil {
		d.CompletedAt = t.CompletedDateTime.DateTime
	}
	ts := fallback
	if m, err := time.Parse(time.RFC3339Nano, t.LastModifiedDateTime); err == nil {
		ts = m
	}
	data, _ := json.Marshal(d) //nolint:errcheck // strings only
	raw, _ := json.Marshal(t)  //nolint:errcheck // a decoded value
	return sdk.Event{ID: id, Type: TypeTask, TS: sdk.FormatTime(ts), Room: e.room(), Text: t.Title, Data: data, Raw: raw}
}

func deletedEvent(id, taskID string, e entry, ts time.Time) sdk.Event {
	data, _ := json.Marshal(taskData{Change: "deleted", TaskID: taskID, ListID: e.ListID}) //nolint:errcheck // strings only
	return sdk.Event{ID: id, Type: TypeTask, TS: sdk.FormatTime(ts), Room: e.room(), Text: "A task was deleted.", Data: data}
}

// baseline runs the first delta query of a list, when it has no deltaLink.
// It emits nothing.
func (a *Adapter) baseline(ctx context.Context, list string) error {
	a.mu.Lock()
	has := a.st.Delta[list] != ""
	a.mu.Unlock()
	if has {
		return nil
	}
	_, link, err := a.cl.Delta(ctx, list, "")
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.st.Delta[list] = link
	return a.saveLocked()
}

// pollAll runs one delta query for each polled list.
func (a *Adapter) pollAll(ctx context.Context, emit sdk.Emitter) error {
	a.mu.Lock()
	seen := map[string]bool{}
	var lists []entry
	for _, e := range a.st.Subs {
		if e.Mode == sdk.ModePoll && !seen[e.ListID] {
			seen[e.ListID] = true
			lists = append(lists, *e)
		}
	}
	a.mu.Unlock()
	var errs []error
	for _, e := range lists {
		errs = append(errs, a.pollList(ctx, e, emit))
	}
	return errors.Join(errs...)
}

// pollList emits the changes since the stored deltaLink. Without one, it
// only makes the baseline.
func (a *Adapter) pollList(ctx context.Context, e entry, emit sdk.Emitter) error {
	a.mu.Lock()
	link := a.st.Delta[e.ListID]
	a.mu.Unlock()
	items, next, err := a.cl.Delta(ctx, e.ListID, link)
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusGone {
		link = ""
		items, next, err = a.cl.Delta(ctx, e.ListID, "")
	}
	if err != nil {
		return err
	}
	if link != "" && emit != nil {
		now := a.now()
		for _, t := range items {
			var ev sdk.Event
			switch {
			case len(t.Removed) > 0:
				ev = deletedEvent("poll:"+t.ID+":deleted", t.ID, e, now)
			case t.CreatedDateTime != "" && t.CreatedDateTime == t.LastModifiedDateTime:
				ev = taskEvent("poll:"+t.ID+":"+t.LastModifiedDateTime, "created", t, e, now)
			default:
				ev = taskEvent("poll:"+t.ID+":"+t.LastModifiedDateTime, "updated", t, e, now)
			}
			if err := emit.Event(ev); err != nil {
				return err
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.st.Delta[e.ListID] = next
	return a.saveLocked()
}
