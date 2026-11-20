// Package fakeplane is an in-process Plane API for `serve --sandbox` and the
// tests. It has one workspace, one project with intake, and the read
// endpoints that the adapter and the operations use.
package fakeplane

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Fixed ids of the sandbox.
const (
	Workspace = "sandbox"
	ProjectID = "11111111-1111-4111-8111-111111111111"
	Project   = "SANDBOX"
	UserID    = "22222222-2222-4222-8222-222222222222"
	BotID     = "33333333-3333-4333-8333-333333333333"
)

// Item is one work item.
type Item struct {
	ID          string
	Seq         int
	Name        string
	State       string // state name
	Labels      []string
	Priority    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CreatedBy   string
	UpdatedBy   string
	Activities  []map[string]any
	Comments    []map[string]any
	IntakeID    string
	IntakeState int
}

// Plane is the fake server. Its clock is the real clock, but it moves at
// least one millisecond on each change.
type Plane struct {
	srv *httptest.Server

	mu        sync.Mutex
	now       time.Time
	items     []*Item
	intake    []map[string]any
	customers []map[string]any
	seq       int
	ids       int
	stop      chan struct{}
}

var states = []map[string]any{
	{"id": "s-backlog", "name": "Backlog", "group": "backlog", "color": "#999999"},
	{"id": "s-todo", "name": "Todo", "group": "unstarted", "color": "#3a3a3a"},
	{"id": "s-progress", "name": "In Progress", "group": "started", "color": "#f59e0b"},
	{"id": "s-done", "name": "Done", "group": "completed", "color": "#16a34a"},
}

var labels = map[string]string{"agent": "l-agent", "bug": "l-bug"}

// Start runs the server. Close stops it.
func Start() *Plane {
	p := &Plane{now: time.Now().UTC(), stop: make(chan struct{})}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	return p
}

// URL is the API host.
func (p *Plane) URL() string { return p.srv.URL }

// Close stops the server and the auto changes.
func (p *Plane) Close() {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	p.srv.Close()
}

// Auto makes one scripted change at each interval, in a loop: create a work
// item, move it, comment on it, add an intake item, accept it.
func (p *Plane) Auto(every time.Duration) {
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		var cur *Item
		var in string
		for n := 0; ; n++ {
			select {
			case <-p.stop:
				return
			case <-t.C:
			}
			switch n % 5 {
			case 0:
				cur = p.Create(fmt.Sprintf("Sandbox task %d", n/5+1), "agent")
			case 1:
				p.SetState(cur.ID, "In Progress")
			case 2:
				p.Comment(cur.ID, "Looks good")
			case 3:
				in = p.AddIntake(fmt.Sprintf("Sandbox request %d", n/5+1))
			case 4:
				p.SetIntake(in, 1)
			}
		}
	}()
}

func (p *Plane) tick() time.Time {
	next := p.now.Add(time.Millisecond)
	if t := time.Now().UTC(); t.After(next) {
		next = t
	}
	p.now = next
	return p.now
}

func (p *Plane) newID() string {
	p.ids++
	return fmt.Sprintf("aaaaaaaa-0000-4000-8000-%012d", p.ids)
}

// Clock returns the current fake time.
func (p *Plane) Clock() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.now
}

// Create adds a work item in Backlog.
func (p *Plane) Create(name string, labelNames ...string) *Item {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.create(name, labelNames...)
}

func (p *Plane) create(name string, labelNames ...string) *Item {
	now := p.tick()
	p.seq++
	it := &Item{
		ID: p.newID(), Seq: p.seq, Name: name, State: "Backlog", Labels: labelNames, Priority: "none",
		CreatedAt: now, UpdatedAt: now, CreatedBy: UserID, UpdatedBy: UserID,
	}
	p.items = append(p.items, it)
	return it
}

func (p *Plane) item(id string) *Item {
	for _, it := range p.items {
		if it.ID == id || fmt.Sprintf("%s-%d", Project, it.Seq) == strings.ToUpper(id) {
			return it
		}
	}
	return nil
}

func (p *Plane) change(it *Item, field, old, cur string) {
	now := p.tick()
	it.UpdatedAt, it.UpdatedBy = now, UserID
	it.Activities = append(it.Activities, map[string]any{
		"id": p.newID(), "actor_id": UserID, "field": field, "verb": "updated",
		"old_value": old, "new_value": cur, "created_at": stamp(now),
	})
}

// SetState moves a work item to the state with the name.
func (p *Plane) SetState(id, name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	it := p.item(id)
	old := it.State
	it.State = name
	p.change(it, "state", old, name)
}

// SetPriority changes the priority of a work item.
func (p *Plane) SetPriority(id, prio string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	it := p.item(id)
	old := it.Priority
	it.Priority = prio
	p.change(it, "priority", old, prio)
}

// AddLabel adds a label to a work item.
func (p *Plane) AddLabel(id, name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	it := p.item(id)
	it.Labels = append(it.Labels, name)
	p.change(it, "labels", "", name)
}

// Comment adds a comment. It does not change updated_at, as in Plane.
func (p *Plane) Comment(id, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	it := p.item(id)
	now := p.tick()
	it.Comments = append(it.Comments, map[string]any{
		"id": p.newID(), "comment_stripped": text, "comment_html": "<p>" + text + "</p>",
		"actor_id": UserID, "created_by_id": UserID, "created_at": stamp(now), "work_item_id": it.ID,
	})
}

// AddIntake adds a pending intake item and its work item. It returns the
// intake id.
func (p *Plane) AddIntake(name string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	it := p.create(name)
	it.CreatedBy, it.UpdatedBy = BotID, BotID
	id := p.newID()
	it.IntakeID, it.IntakeState = id, -2
	p.intake = append(p.intake, map[string]any{
		"id": id, "name": name, "status": -2, "work_item_id": it.ID, "source": "IN_APP", "priority": "none",
		"created_at": stamp(it.CreatedAt), "updated_at": stamp(it.CreatedAt), "created_by_id": BotID, "updated_by_id": BotID,
	})
	return id
}

// SetIntake sets the status of an intake item.
func (p *Plane) SetIntake(id string, status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.tick()
	for _, in := range p.intake {
		if in["id"] == id {
			in["status"], in["updated_at"], in["updated_by_id"] = status, stamp(now), UserID
			if it := p.item(in["work_item_id"].(string)); it != nil {
				it.IntakeState = status
			}
		}
	}
}

// UpdateCustomer adds or changes a customer.
func (p *Plane) UpdateCustomer(name, stage string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.tick()
	for _, c := range p.customers {
		if c["name"] == name {
			c["stage"], c["updated_at"] = stage, stamp(now)
			return
		}
	}
	p.customers = append(p.customers, map[string]any{
		"id": p.newID(), "name": name, "stage": stage, "created_at": stamp(now), "updated_at": stamp(now),
		"created_by_id": UserID, "updated_by_id": UserID,
	})
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000Z07:00") }

func stateOf(name string) map[string]any {
	for _, s := range states {
		if s["name"] == name {
			return s
		}
	}
	return states[0]
}

func (it *Item) json() map[string]any {
	ls := []map[string]any{}
	ids := []string{}
	for _, l := range it.Labels {
		ls = append(ls, map[string]any{"id": labels[l], "name": l})
		ids = append(ids, labels[l])
	}
	st := stateOf(it.State)
	return map[string]any{
		"id": it.ID, "identifier": fmt.Sprintf("%s-%d", Project, it.Seq), "name": it.Name, "project_id": ProjectID,
		"sequence_id": it.Seq, "state_id": st["id"], "state": st, "label_ids": ids, "labels": ls,
		"priority": it.Priority, "assignee_ids": []string{}, "parent_id": nil, "target_date": nil,
		"created_at": stamp(it.CreatedAt), "updated_at": stamp(it.UpdatedAt),
		"created_by_id": it.CreatedBy, "updated_by_id": it.UpdatedBy,
	}
}

func (p *Plane) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Api-Key") == "" {
		reply(w, http.StatusUnauthorized, map[string]any{"detail": "Authentication credentials were not provided."})
		return
	}
	if r.Method != http.MethodGet {
		reply(w, http.StatusMethodNotAllowed, map[string]any{"detail": "read only"})
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	q := r.URL.Query()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// parts: api v2 workspaces <ws> ...
	if len(parts) == 4 && parts[2] == "users" && parts[3] == "me" {
		reply(w, http.StatusOK, map[string]any{"id": UserID, "display_name": "ana", "email": "ana@example.com"})
		return
	}
	if len(parts) < 5 || parts[2] != "workspaces" || parts[3] != Workspace {
		reply(w, http.StatusNotFound, map[string]any{"detail": "Not found."})
		return
	}
	rest := parts[4:]
	switch {
	case len(rest) == 1 && rest[0] == "projects":
		list(w, q, []any{map[string]any{"id": ProjectID, "identifier": Project, "name": "Sandbox", "intake_view": true}})
	case len(rest) == 2 && rest[0] == "projects" && (rest[1] == Project || rest[1] == ProjectID):
		reply(w, http.StatusOK, map[string]any{"id": ProjectID, "identifier": Project, "name": "Sandbox", "intake_view": true})
	case len(rest) == 1 && rest[0] == "members":
		list(w, q, []any{
			map[string]any{"member_id": UserID, "member": map[string]any{"id": UserID, "display_name": "ana", "email": "ana@example.com"}},
			map[string]any{"member_id": BotID, "member": map[string]any{"id": BotID, "display_name": "Intake", "email": "sandbox-intake@plane.so"}},
		})
	case len(rest) == 1 && rest[0] == "customers":
		list(w, q, sorted(p.customers, q.Get("order_by")))
	case len(rest) == 2 && rest[0] == "work-items" && rest[1] == "search":
		p.search(w, q)
	case len(rest) == 1 && rest[0] == "work-items", len(rest) == 3 && rest[0] == "projects" && rest[2] == "work-items":
		p.listItems(w, q)
	case len(rest) == 2 && rest[0] == "work-items", len(rest) == 4 && rest[0] == "projects" && rest[2] == "work-items":
		it := p.item(rest[len(rest)-1])
		if it == nil {
			reply(w, http.StatusNotFound, map[string]any{"detail": "Not found."})
			return
		}
		reply(w, http.StatusOK, it.json())
	case len(rest) == 5 && rest[0] == "projects" && rest[2] == "work-items" && (rest[4] == "activities" || rest[4] == "comments"):
		it := p.item(rest[3])
		if it == nil {
			reply(w, http.StatusNotFound, map[string]any{"detail": "Not found."})
			return
		}
		rows, field := it.Activities, "created_at"
		if rest[4] == "comments" {
			rows = it.Comments
		}
		var out []map[string]any
		for _, a := range rows {
			if g := q.Get("created_at__gte"); g == "" || a[field].(string) >= g {
				out = append(out, a)
			}
		}
		list(w, q, sorted(out, q.Get("order_by")))
	case len(rest) == 3 && rest[0] == "projects" && rest[2] == "intake-issues":
		list(w, q, sorted(p.intake, q.Get("order_by")))
	default:
		reply(w, http.StatusNotFound, map[string]any{"detail": "Not found."})
	}
}

func (p *Plane) listItems(w http.ResponseWriter, q map[string][]string) {
	gte := ""
	if v := q["updated_at__gte"]; len(v) > 0 {
		gte = v[0]
	}
	var rows []map[string]any
	for _, it := range p.items {
		if gte != "" && stamp(it.UpdatedAt) < gte {
			continue
		}
		rows = append(rows, it.json())
	}
	order := ""
	if v := q["order_by"]; len(v) > 0 {
		order = v[0]
	}
	list(w, q, sorted(rows, order))
}

func (p *Plane) search(w http.ResponseWriter, q map[string][]string) {
	term := ""
	if v := q["search"]; len(v) > 0 {
		term = strings.ToLower(v[0])
	}
	out := []map[string]any{}
	for _, it := range p.items {
		if strings.Contains(strings.ToLower(it.Name), term) {
			out = append(out, map[string]any{
				"id": it.ID, "name": it.Name, "project__identifier": Project, "project_id": ProjectID,
				"sequence_id": it.Seq, "workspace__slug": Workspace,
			})
		}
	}
	reply(w, http.StatusOK, map[string]any{"issues": out})
}

// sorted orders rows by the field of order_by; a leading "-" is descending.
func sorted(rows []map[string]any, order string) []any {
	field, desc := strings.TrimPrefix(order, "-"), strings.HasPrefix(order, "-")
	cp := slices.Clone(rows)
	if field != "" {
		slices.SortStableFunc(cp, func(a, b map[string]any) int {
			c := strings.Compare(fmt.Sprint(a[field]), fmt.Sprint(b[field]))
			if desc {
				return -c
			}
			return c
		})
	}
	out := make([]any, len(cp))
	for i, r := range cp {
		out[i] = r
	}
	return out
}

// list writes the v2 offset envelope.
func list(w http.ResponseWriter, q map[string][]string, rows []any) {
	per := 100
	if v := q["per_page"]; len(v) > 0 {
		if n, err := strconv.Atoi(v[0]); err == nil && n > 0 {
			per = n
		}
	}
	off := 0
	if v := q["offset"]; len(v) > 0 {
		off, _ = strconv.Atoi(v[0])
	}
	off = min(off, len(rows))
	end := min(off+per, len(rows))
	env := map[string]any{"data": rows[off:end], "total_count": len(rows), "pagination": map[string]any{"style": "offset"}}
	if end < len(rows) {
		env["next"] = end
	} else {
		env["next"] = nil
	}
	reply(w, http.StatusOK, env)
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck // test server
}
