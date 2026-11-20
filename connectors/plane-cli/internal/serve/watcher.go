package serve

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/trebi-ai/trebi-connectors/sdk"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/listen"
)

// Poll limits. Plane allows about 60 requests per minute for one key.
const (
	hotWindow      = 72 * time.Hour   // a work item gets comment checks this long after a change
	commentChecks  = 10               // comment lists per tick
	maxItems       = 3000             // snapshots kept per workspace
	refreshEvery   = 10 * time.Minute // project and member lists
	customersPause = time.Hour        // after the customer list fails
	seedLimit      = 50
)

// itemFields are the work item fields that the poll reads.
const itemFields = "id,identifier,name,project_id,sequence_id,state_id,label_ids,assignee_ids,priority,parent_id," +
	"target_date,created_at,updated_at,created_by_id,updated_by_id"

// wsState is the saved state of one workspace. It holds no paths.
type wsState struct {
	Cursor     string                `json:"cursor,omitempty"`
	Items      map[string]*snap      `json:"items"`
	Intake     map[string]intakeSnap `json:"intake"`
	IntakeBase map[string]bool       `json:"intake_base"` // project id → first list done
	Customers  string                `json:"customers,omitempty"`
}

// intakeSnap is the last status of one intake item.
type intakeSnap struct {
	Status   int    `json:"s"`
	WorkItem string `json:"w,omitempty"`
}

func newWSState() *wsState {
	return &wsState{Items: map[string]*snap{}, Intake: map[string]intakeSnap{}, IntakeBase: map[string]bool{}}
}

// watcher polls one Plane workspace.
type watcher struct {
	ws    string
	cl    *client.Client
	scope Scope
	st    *wsState
	m     mapper
	log   *slog.Logger
	now   func() time.Time

	projects  map[string]project // id → project
	projAt    time.Time
	membersAt time.Time
	poller    *listen.Poller
	pollIDs   string
	checked   map[string]time.Time // work item id → last comment check
	custOff   time.Time

	// emit is the emitter of the current tick; Emit (the poller sink) uses it.
	emit sdk.Emitter
}

func newWatcher(ws string, cl *client.Client, scope Scope, st *wsState, web string, log *slog.Logger) *watcher {
	return &watcher{
		ws: ws, cl: cl, scope: scope, st: st, log: log.With("workspace", ws), now: time.Now,
		m:        mapper{ws: ws, web: web, members: map[string]member{}},
		projects: map[string]project{}, checked: map[string]time.Time{},
	}
}

func (w *watcher) path(format string, args ...any) string {
	return "/api/v2/workspaces/" + url.PathEscape(w.ws) + fmt.Sprintf(format, args...)
}

// tick runs one poll of each source. It goes on after a failed source and
// returns the first error.
func (w *watcher) tick(ctx context.Context, e sdk.Emitter) error {
	w.emit = e
	if err := w.loadProjects(ctx, false); err != nil {
		return err
	}
	if w.membersAt.IsZero() {
		w.loadMembers(ctx)
	}
	errs := []error{w.pollIntake(ctx), w.pollItems(ctx), w.pollComments(ctx)}
	if w.scope.All(w.ws) {
		errs = append(errs, w.pollCustomers(ctx))
	}
	w.prune()
	return errors.Join(errs...)
}

func (w *watcher) loadProjects(ctx context.Context, force bool) error {
	if !force && w.now().Sub(w.projAt) < refreshEvery {
		return nil
	}
	if force && w.now().Sub(w.projAt) < time.Minute {
		return nil
	}
	items, err := w.cl.ListAll(ctx, w.path("/projects/"), url.Values{"fields": {"id,identifier,name,intake_view"}}, true, 0)
	if err != nil {
		return err
	}
	ps := map[string]project{}
	for _, raw := range items {
		var p project
		if json.Unmarshal(raw, &p) == nil && p.ID != "" {
			ps[p.ID] = p
		}
	}
	w.projects, w.projAt = ps, w.now()
	if p, ok := w.scope[w.ws]; ok && p != nil {
		for _, ident := range p {
			if !slices.ContainsFunc(w.inScope(), func(x project) bool { return x.Identifier == ident }) {
				w.log.Warn("the workspace has no project with this identifier", "project", ident)
			}
		}
	}
	return nil
}

// loadMembers reads the member names. A failure keeps the ids as names.
func (w *watcher) loadMembers(ctx context.Context) {
	w.membersAt = w.now()
	items, err := w.cl.ListAll(ctx, w.path("/members/"), url.Values{"expand": {"member"}}, true, 0)
	if err != nil {
		w.log.Warn("list members", "err", err)
		return
	}
	for _, raw := range items {
		var r struct {
			MemberID string `json:"member_id"`
			Member   struct {
				DisplayName string `json:"display_name"`
				Email       string `json:"email"`
			} `json:"member"`
		}
		if json.Unmarshal(raw, &r) == nil && r.MemberID != "" {
			w.m.members[r.MemberID] = member{Name: cmp.Or(r.Member.DisplayName, r.Member.Email), Email: r.Member.Email}
		}
	}
}

// knowActor reloads the members once when an actor is new.
func (w *watcher) knowActor(ctx context.Context, id string) {
	if _, ok := w.m.members[id]; id == "" || ok || w.now().Sub(w.membersAt) < refreshEvery {
		return
	}
	w.loadMembers(ctx)
}

// inScope returns the projects in the scope, sorted by identifier.
func (w *watcher) inScope() []project {
	var out []project
	for _, p := range w.projects {
		if w.scope.Has(w.ws, p.Identifier) {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b project) int { return strings.Compare(a.Identifier, b.Identifier) })
	return out
}

// project returns the project of id when it is in the scope. An unknown id
// reloads the project list, at most once a minute.
func (w *watcher) project(ctx context.Context, id string) (project, bool) {
	p, ok := w.projects[id]
	if !ok {
		if err := w.loadProjects(ctx, true); err != nil {
			w.log.Warn("list projects", "err", err)
		}
		p, ok = w.projects[id]
	}
	return p, ok && w.scope.Has(w.ws, p.Identifier)
}

// pollIntake compares the intake lists with the saved statuses. The first
// list of a project only saves the statuses.
func (w *watcher) pollIntake(ctx context.Context) error {
	var errs []error
	for _, p := range w.inScope() {
		if !p.Intake {
			continue
		}
		q := url.Values{"order_by": {"-updated_at"}, "per_page": {"50"}}
		items, err := w.cl.ListAll(ctx, w.path("/projects/%s/intake-issues/", p.ID), q, true, 50)
		if err != nil {
			errs = append(errs, fmt.Errorf("intake %s: %w", p.Identifier, err))
			continue
		}
		base := w.st.IntakeBase[p.ID]
		slices.Reverse(items) // oldest first
		for _, raw := range items {
			var in intakeItem
			if json.Unmarshal(raw, &in) != nil || in.ID == "" {
				continue
			}
			old, known := w.st.Intake[in.ID]
			if known && old.Status == in.Status {
				continue
			}
			if base {
				var prev *int
				if known {
					prev = &old.Status
				}
				if err := w.sendIntake(ctx, p, in, prev); err != nil {
					return errors.Join(append(errs, err)...)
				}
			}
			w.st.Intake[in.ID] = intakeSnap{Status: in.Status, WorkItem: in.WorkItemID}
		}
		w.st.IntakeBase[p.ID] = true
	}
	return errors.Join(errs...)
}

func (w *watcher) sendIntake(ctx context.Context, p project, in intakeItem, prev *int) error {
	k := ""
	if in.WorkItemID != "" {
		var it workItem
		if err := w.cl.GetJSON(ctx, w.path("/projects/%s/work-items/%s/", p.ID, in.WorkItemID), url.Values{"fields": {"id,sequence_id"}}, &it); err == nil {
			k = key(p, it)
		}
	}
	w.knowActor(ctx, cmp.Or(in.UpdatedByID, in.CreatedByID))
	ev, ok := w.m.intakeEvent(p, in, prev, k)
	if !ok {
		return nil
	}
	return w.emit.Event(ev)
}

// held reports a work item that waits in intake: it is not a task yet.
func (w *watcher) held(itemID string) bool {
	for _, in := range w.st.Intake {
		if in.WorkItem == itemID {
			return in.Status != IntakeAccepted
		}
	}
	return false
}

// pollItems runs the work item poller. The first run of a workspace saves
// the items of the last days and sends nothing.
func (w *watcher) pollItems(ctx context.Context) error {
	q := url.Values{"fields": {itemFields}, "expand": {"state,labels"}}
	if !w.scope.All(w.ws) {
		var ids []string
		for _, p := range w.inScope() {
			ids = append(ids, p.ID)
		}
		if len(ids) == 0 {
			return nil
		}
		q.Set("project_id__in", strings.Join(ids, ","))
	}
	if w.poller != nil && w.pollIDs != q.Get("project_id__in") {
		w.poller = nil
	}
	if w.poller == nil {
		if w.st.Cursor == "" {
			if err := w.seed(ctx, q); err != nil {
				return err
			}
		}
		since, err := time.Parse(time.RFC3339Nano, w.st.Cursor)
		if err != nil {
			return fmt.Errorf("saved cursor %q: %w", w.st.Cursor, err)
		}
		p := listen.NewPoller(w.cl, listen.NewProjects(w.cl, w.ws), w, nil, w.ws, w.path("/work-items/"), q)
		p.Activities = true
		p.Since = since
		if err := p.Start(ctx); err != nil {
			return err
		}
		w.poller, w.pollIDs = p, q.Get("project_id__in")
	} else if err := w.poller.Poll(ctx); err != nil {
		return err
	}
	w.st.Cursor = cmp.Or(w.poller.Cursor(), w.st.Cursor)
	return nil
}

// seed saves the items of the last days, so that the first changes have old
// values and the comment checks have work items.
func (w *watcher) seed(ctx context.Context, q url.Values) error {
	sq := url.Values{}
	for k, v := range q {
		sq[k] = v
	}
	sq.Set("order_by", "-updated_at")
	sq.Set("per_page", fmt.Sprint(seedLimit))
	sq.Set("updated_at__gte", sdk.FormatTime(w.now().Add(-hotWindow)))
	var page struct {
		Data []workItem `json:"data"`
	}
	if err := w.cl.GetJSON(ctx, w.path("/work-items/"), sq, &page); err != nil {
		return err
	}
	start := sdk.FormatTime(w.now())
	w.st.Cursor = start
	for i, it := range page.Data {
		if i == 0 {
			w.st.Cursor = it.UpdatedAt
		}
		s := it.snapshot()
		s.Comments = start // older comments are not news
		w.st.Items[it.ID] = &s
	}
	return nil
}

// Emit takes one change of the poller.
func (w *watcher) Emit(ev listen.Event) error {
	ctx := context.Background()
	raw, err := json.Marshal(ev.Data)
	if err != nil {
		return err
	}
	var it workItem
	if err := json.Unmarshal(raw, &it); err != nil || it.ID == "" {
		return nil
	}
	p, ok := w.project(ctx, it.ProjectID)
	if !ok {
		return nil
	}
	prev := w.st.Items[it.ID]
	if prev != nil && prev.U == it.UpdatedAt {
		return nil
	}
	if w.held(it.ID) {
		// Keep the snapshot, so that the item is not new when intake accepts it.
		s := it.snapshot()
		s.Comments = sdk.FormatTime(w.now())
		w.st.Items[it.ID] = &s
		return nil
	}
	var acts []activity
	for _, r := range ev.Activities {
		var a activity
		if json.Unmarshal(r, &a) == nil {
			acts = append(acts, a)
			w.knowActor(ctx, a.ActorID)
		}
	}
	w.knowActor(ctx, it.UpdatedByID)
	created := prev == nil && ev.Event == "workitem.created"
	for _, e := range w.m.workItemEvents(p, it, prev, acts, created) {
		if err := w.emit.Event(e); err != nil {
			return err
		}
	}
	s := it.snapshot()
	switch {
	case prev != nil:
		s.Comments = prev.Comments
	case created:
		s.Comments = it.CreatedAt
	default:
		s.Comments = it.UpdatedAt
	}
	w.st.Items[it.ID] = &s
	delete(w.checked, it.ID) // check its comments soon
	return nil
}

// pollComments lists the comments of the work items that changed in the
// last days. Each tick checks the items with the oldest check first.
func (w *watcher) pollComments(ctx context.Context) error {
	cut := sdk.FormatTime(w.now().Add(-hotWindow))
	var hot []string
	for id, s := range w.st.Items {
		if s.U >= cut {
			hot = append(hot, id)
		}
	}
	slices.SortFunc(hot, func(a, b string) int {
		return cmp.Or(w.checked[a].Compare(w.checked[b]), strings.Compare(w.st.Items[b].U, w.st.Items[a].U))
	})
	var errs []error
	for _, id := range hot[:min(len(hot), commentChecks)] {
		w.checked[id] = w.now()
		if err := w.checkComments(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (w *watcher) checkComments(ctx context.Context, id string) error {
	s := w.st.Items[id]
	p, ok := w.project(ctx, s.P)
	if !ok || w.held(id) {
		return nil
	}
	q := url.Values{"order_by": {"-created_at"}, "per_page": {"20"}}
	items, err := w.cl.ListAll(ctx, w.path("/projects/%s/work-items/%s/comments/", p.ID, id), q, true, 20)
	var ae *client.APIError
	if errors.As(err, &ae) && ae.StatusCode == http.StatusNotFound {
		delete(w.st.Items, id) // deleted or archived
		return nil
	}
	if err != nil {
		return err
	}
	slices.Reverse(items) // oldest first
	k := ""
	for _, raw := range items {
		var c comment
		if json.Unmarshal(raw, &c) != nil || c.ID == "" || c.CreatedAt <= s.Comments {
			continue
		}
		if k == "" {
			k = w.itemKey(ctx, p, id)
		}
		w.knowActor(ctx, cmp.Or(c.ActorID, c.CreatedByID))
		if err := w.emit.Event(w.m.commentEvent(p, *s, id, k, c)); err != nil {
			return err
		}
		s.Comments = c.CreatedAt
	}
	return nil
}

// itemKey reads the key ENG-42 of a work item.
func (w *watcher) itemKey(ctx context.Context, p project, id string) string {
	var it workItem
	if err := w.cl.GetJSON(ctx, w.path("/projects/%s/work-items/%s/", p.ID, id), url.Values{"fields": {"id,sequence_id"}}, &it); err != nil {
		return ""
	}
	return key(p, it)
}

// pollCustomers sends the customers that changed after the saved time.
// A workspace without the customer feature pauses the poll for an hour.
func (w *watcher) pollCustomers(ctx context.Context) error {
	if w.now().Before(w.custOff) {
		return nil
	}
	q := url.Values{"order_by": {"-updated_at"}, "per_page": {"20"}}
	items, err := w.cl.ListAll(ctx, w.path("/customers/"), q, true, 20)
	var ae *client.APIError
	if errors.As(err, &ae) && ae.StatusCode >= 400 && ae.StatusCode < 500 && ae.StatusCode != http.StatusTooManyRequests && ae.StatusCode != http.StatusUnauthorized {
		w.custOff = w.now().Add(customersPause)
		w.log.Info("customers are not available; pause the customer poll", "status", ae.StatusCode)
		return nil
	}
	if err != nil {
		return err
	}
	slices.Reverse(items)
	first := w.st.Customers == ""
	newest := w.st.Customers
	for _, raw := range items {
		var c customer
		if json.Unmarshal(raw, &c) != nil || c.ID == "" || c.UpdatedAt <= w.st.Customers {
			continue
		}
		if !first {
			w.knowActor(ctx, c.UpdatedByID)
			if err := w.emit.Event(w.m.customerEvent(c)); err != nil {
				return err
			}
		}
		newest = max(newest, c.UpdatedAt)
	}
	w.st.Customers = cmp.Or(newest, sdk.FormatTime(w.now()))
	return nil
}

// prune drops the oldest snapshots above maxItems.
func (w *watcher) prune() {
	if len(w.st.Items) <= maxItems {
		return
	}
	ids := make([]string, 0, len(w.st.Items))
	for id := range w.st.Items {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int { return strings.Compare(w.st.Items[b].U, w.st.Items[a].U) })
	for _, id := range ids[maxItems:] {
		delete(w.st.Items, id)
		delete(w.checked, id)
	}
}
