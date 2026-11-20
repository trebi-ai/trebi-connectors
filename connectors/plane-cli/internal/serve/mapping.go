package serve

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Event types.
const (
	TypeCreated        = "work_item.created"
	TypeUpdated        = "work_item.updated"
	TypeStateChanged   = "work_item.state_changed"
	TypeComment        = "comment.created"
	TypeIntakeCreated  = "intake.created"
	TypeIntakeAccepted = "intake.accepted"
	TypeIntakeDeclined = "intake.declined"
	TypeIntakeDup      = "intake.duplicate"
	TypeIntakeSnoozed  = "intake.snoozed"
	TypeCustomer       = "customer.updated"
)

// Events is what the adapter declares.
var Events = []sdk.EventDecl{
	{Type: TypeCreated}, {Type: TypeUpdated}, {Type: TypeStateChanged}, {Type: TypeComment},
	{Type: TypeIntakeCreated}, {Type: TypeIntakeAccepted}, {Type: TypeIntakeDeclined},
	{Type: TypeIntakeDup}, {Type: TypeIntakeSnoozed}, {Type: TypeCustomer},
}

// Intake statuses of the Plane API.
const (
	IntakePending   = -2
	IntakeDeclined  = -1
	IntakeSnoozed   = 0
	IntakeAccepted  = 1
	IntakeDuplicate = 2
)

// IntakeStatus names a Plane intake status.
func IntakeStatus(s int) string {
	switch s {
	case IntakePending:
		return "pending"
	case IntakeDeclined:
		return "declined"
	case IntakeSnoozed:
		return "snoozed"
	case IntakeAccepted:
		return "accepted"
	case IntakeDuplicate:
		return "duplicate"
	}
	return fmt.Sprintf("status %d", s)
}

// intakeType is the event type of a move to status s, or "" for none.
func intakeType(s int) string {
	switch s {
	case IntakeAccepted:
		return TypeIntakeAccepted
	case IntakeDeclined:
		return TypeIntakeDeclined
	case IntakeDuplicate:
		return TypeIntakeDup
	case IntakeSnoozed:
		return TypeIntakeSnoozed
	}
	return ""
}

// project is one Plane project in the scope.
type project struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Name       string `json:"name"`
	Intake     bool   `json:"intake_view"`
}

// member is one workspace member.
type member struct {
	Name  string
	Email string
}

// Bot reports a Plane system user, such as the intake or the Plane bot.
func (m member) Bot() bool {
	return strings.HasPrefix(m.Email, "bot_user_") || strings.Contains(m.Email, "-intake@")
}

// workItem is one row of the work item list with state and labels expanded.
type workItem struct {
	ID          string   `json:"id"`
	Identifier  string   `json:"identifier"`
	Name        string   `json:"name"`
	ProjectID   string   `json:"project_id"`
	SequenceID  int      `json:"sequence_id"`
	StateID     string   `json:"state_id"`
	State       *state   `json:"state"`
	Labels      []label  `json:"labels"`
	Priority    string   `json:"priority"`
	AssigneeIDs []string `json:"assignee_ids"`
	ParentID    string   `json:"parent_id"`
	TargetDate  string   `json:"target_date"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
	CreatedByID string   `json:"created_by_id"`
	UpdatedByID string   `json:"updated_by_id"`
}

type state struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Group string `json:"group"`
}

type label struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (it workItem) labelNames() []string {
	out := make([]string, 0, len(it.Labels))
	for _, l := range it.Labels {
		out = append(out, l.Name)
	}
	slices.Sort(out)
	return out
}

func (it workItem) stateName() string {
	if it.State == nil {
		return ""
	}
	return it.State.Name
}

func (it workItem) stateGroup() string {
	if it.State == nil {
		return ""
	}
	return it.State.Group
}

// snapshot keeps the fields that the adapter compares.
func (it workItem) snapshot() snap {
	a := slices.Clone(it.AssigneeIDs)
	slices.Sort(a)
	return snap{
		U: it.UpdatedAt, P: it.ProjectID, Name: it.Name, StateID: it.StateID, State: it.stateName(), Group: it.stateGroup(),
		Labels: it.labelNames(), Priority: it.Priority, Assignees: a, Parent: it.ParentID, Target: it.TargetDate,
	}
}

// snap is what the adapter keeps of one work item, to find the old values
// after a change. Comments is the created_at of the newest comment seen.
type snap struct {
	U         string   `json:"u"`
	P         string   `json:"p"`
	Name      string   `json:"name,omitempty"`
	StateID   string   `json:"state_id,omitempty"`
	State     string   `json:"state,omitempty"`
	Group     string   `json:"group,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Priority  string   `json:"priority,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
	Parent    string   `json:"parent,omitempty"`
	Target    string   `json:"target,omitempty"`
	Comments  string   `json:"comments,omitempty"`
}

// diff lists the fields that differ from the previous snapshot.
func (s snap) diff(prev snap) []change {
	var out []change
	add := func(field string, old, cur any) { out = append(out, change{Field: field, Old: old, New: cur}) }
	if prev.Name != s.Name {
		add("name", prev.Name, s.Name)
	}
	if prev.Priority != s.Priority {
		add("priority", prev.Priority, s.Priority)
	}
	if !slices.Equal(prev.Labels, s.Labels) {
		add("labels", prev.Labels, s.Labels)
	}
	if !slices.Equal(prev.Assignees, s.Assignees) {
		add("assignees", prev.Assignees, s.Assignees)
	}
	if prev.Parent != s.Parent {
		add("parent", prev.Parent, s.Parent)
	}
	if prev.Target != s.Target {
		add("target_date", prev.Target, s.Target)
	}
	return out
}

// activity is one row of the work item activity list.
type activity struct {
	ID        string  `json:"id"`
	ActorID   string  `json:"actor_id"`
	Field     *string `json:"field"`
	Verb      string  `json:"verb"`
	OldValue  *string `json:"old_value"`
	NewValue  *string `json:"new_value"`
	CreatedAt string  `json:"created_at"`
}

func (a activity) field() string {
	if a.Field == nil {
		return ""
	}
	return *a.Field
}

// change is one field change.
type change struct {
	Field string `json:"field"`
	Old   any    `json:"old"`
	New   any    `json:"new"`
}

// intakeItem is one row of the intake list.
type intakeItem struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Status        int    `json:"status"`
	WorkItemID    string `json:"work_item_id"`
	DuplicateToID string `json:"duplicate_to_id"`
	SnoozedTill   string `json:"snoozed_till"`
	Source        string `json:"source"`
	Priority      string `json:"priority"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	CreatedByID   string `json:"created_by_id"`
	UpdatedByID   string `json:"updated_by_id"`
}

// comment is one row of the comment list.
type comment struct {
	ID          string `json:"id"`
	Stripped    string `json:"comment_stripped"`
	HTML        string `json:"comment_html"`
	ActorID     string `json:"actor_id"`
	CreatedByID string `json:"created_by_id"`
	CreatedAt   string `json:"created_at"`
}

// customer is one row of the customer list.
type customer struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Domain         string `json:"domain"`
	Stage          string `json:"stage"`
	ContractStatus string `json:"contract_status"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	UpdatedByID    string `json:"updated_by_id"`
	CreatedByID    string `json:"created_by_id"`
}

// mapper turns Plane changes of one workspace into events.
type mapper struct {
	ws      string
	web     string // web app root, for links; empty gives no url
	members map[string]member
}

func (m mapper) room(p project) *sdk.Room {
	return &sdk.Room{ID: m.ws + "/" + p.Identifier, Name: cmp.Or(p.Name, p.Identifier), Kind: "project"}
}

func (m mapper) sender(id string) *sdk.Author {
	if id == "" {
		return nil
	}
	mb, ok := m.members[id]
	if !ok {
		return &sdk.Author{ID: id}
	}
	return &sdk.Author{ID: id, Name: mb.Name, Bot: mb.Bot()}
}

func (m mapper) url(key string) string {
	if m.web == "" || key == "" {
		return ""
	}
	return m.web + "/" + m.ws + "/browse/" + key + "/"
}

func key(p project, it workItem) string {
	if strings.Contains(it.Identifier, "-") {
		return it.Identifier
	}
	if p.Identifier != "" && it.SequenceID > 0 {
		return fmt.Sprintf("%s-%d", p.Identifier, it.SequenceID)
	}
	return it.Identifier
}

// workItemEvents maps one changed work item. prev is the last snapshot, or
// nil when the adapter did not see the item before. acts are the activity
// rows of the change.
func (m mapper) workItemEvents(p project, it workItem, prev *snap, acts []activity, created bool) []sdk.Event {
	k := key(p, it)
	base := func() map[string]any {
		return map[string]any{
			"workspace": m.ws, "project": p.Identifier, "work_item_id": it.ID, "identifier": k, "name": it.Name,
			"url": m.url(k), "state": it.stateName(), "state_group": it.stateGroup(), "labels": it.labelNames(),
			"priority": it.Priority,
		}
	}
	actor := it.UpdatedByID
	for _, a := range acts {
		actor = cmp.Or(a.ActorID, actor)
	}
	ev := func(typ, text string, data map[string]any) sdk.Event {
		return sdk.Event{
			ID: "wi:" + it.ID + "@" + it.UpdatedAt + ":" + typ, Type: typ, TS: stamp(it.UpdatedAt),
			Room: m.room(p), Sender: m.sender(cmp.Or(actor, it.CreatedByID)), Text: text, Data: encode(data),
		}
	}
	title := k + " " + short(it.Name, 80)
	if created {
		d := base()
		d["field"], d["old"], d["new"] = "", nil, nil
		return []sdk.Event{ev(TypeCreated, "Created "+title, d)}
	}

	var out []sdk.Event
	oldState, oldGroup, stateMoved := "", "", false
	if prev != nil && prev.StateID != "" && prev.StateID != it.StateID {
		oldState, oldGroup, stateMoved = prev.State, prev.Group, true
	}
	var changes []change
	for _, a := range acts {
		switch f := a.field(); f {
		case "", "comment":
		case "state":
			stateMoved = true
			if oldState == "" {
				oldState = deref(a.OldValue)
			}
		default:
			changes = append(changes, change{Field: f, Old: nilIfEmpty(a.OldValue), New: nilIfEmpty(a.NewValue)})
		}
	}
	if len(changes) == 0 && prev != nil {
		changes = it.snapshot().diff(*prev)
	}
	if stateMoved {
		d := base()
		d["field"], d["old"], d["new"] = "state", oldState, it.stateName()
		d["old_state_group"] = oldGroup
		out = append(out, ev(TypeStateChanged, title+": "+cmp.Or(oldState, "?")+" → "+it.stateName(), d))
	}
	if len(changes) > 0 || !stateMoved {
		d := base()
		d["field"], d["old"], d["new"] = "", nil, nil
		text := title + " changed"
		if len(changes) > 0 {
			c := changes[0]
			d["field"], d["old"], d["new"] = c.Field, c.Old, c.New
			text = title + ": " + summary(changes)
		}
		d["changes"] = changes
		out = append(out, ev(TypeUpdated, text, d))
	}
	return out
}

// summary reads "priority none → high" for one change, else the field names.
func summary(cs []change) string {
	if len(cs) == 1 {
		c := cs[0]
		o, n := value(c.Old), value(c.New)
		if c.Field == "description" || len(o) > 40 || len(n) > 40 {
			return c.Field + " changed"
		}
		return c.Field + " " + cmp.Or(o, "none") + " → " + cmp.Or(n, "none")
	}
	names := make([]string, 0, len(cs))
	for _, c := range cs {
		if !slices.Contains(names, c.Field) {
			names = append(names, c.Field)
		}
	}
	return strings.Join(names, ", ") + " changed"
}

func value(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []string:
		return strings.Join(t, ", ")
	}
	return fmt.Sprint(v)
}

// intakeEvent maps a new intake item (prev nil) or a status change. k is
// the key of its work item, when known. ok is false when the change gives
// no event, as a move back to pending.
func (m mapper) intakeEvent(p project, in intakeItem, prev *int, k string) (sdk.Event, bool) {
	typ := TypeIntakeCreated
	if prev != nil {
		if typ = intakeType(in.Status); typ == "" {
			return sdk.Event{}, false
		}
	}
	d := map[string]any{
		"workspace": m.ws, "project": p.Identifier, "intake_id": in.ID, "work_item_id": in.WorkItemID,
		"identifier": k, "name": in.Name, "url": m.url(k), "status": IntakeStatus(in.Status), "source": in.Source,
		"priority": in.Priority, "field": "status", "new": IntakeStatus(in.Status), "old": nil,
	}
	if prev != nil {
		d["old"] = IntakeStatus(*prev)
	}
	if in.DuplicateToID != "" {
		d["duplicate_to"] = in.DuplicateToID
	}
	if in.SnoozedTill != "" {
		d["snoozed_till"] = in.SnoozedTill
	}
	name := short(in.Name, 80)
	if k != "" {
		name = k + " " + name
	}
	text := "New intake item in " + p.Identifier + ": " + name
	if prev != nil {
		text = "Intake item " + IntakeStatus(in.Status) + ": " + name
	}
	return sdk.Event{
		ID: "intake:" + in.ID + "@" + in.UpdatedAt + ":" + IntakeStatus(in.Status), Type: typ, TS: stamp(in.UpdatedAt),
		Room: m.room(p), Sender: m.sender(cmp.Or(in.UpdatedByID, in.CreatedByID)), Text: text, Data: encode(d),
	}, true
}

// commentEvent maps a new comment on a work item.
func (m mapper) commentEvent(p project, it snap, itemID, k string, c comment) sdk.Event {
	body := strings.TrimSpace(c.Stripped)
	d := map[string]any{
		"workspace": m.ws, "project": p.Identifier, "work_item_id": itemID, "identifier": k, "name": it.Name,
		"url": m.url(k), "comment_id": c.ID, "comment": body, "state": it.State, "state_group": it.Group,
		"labels": it.Labels, "field": "comment", "old": nil, "new": body,
	}
	return sdk.Event{
		ID: "comment:" + c.ID, Type: TypeComment, TS: stamp(c.CreatedAt), Room: m.room(p),
		Sender: m.sender(cmp.Or(c.ActorID, c.CreatedByID)), Text: k + ": " + short(body, 280), Data: encode(d),
	}
}

// customerEvent maps a changed customer. The room is the workspace.
func (m mapper) customerEvent(c customer) sdk.Event {
	d := map[string]any{
		"workspace": m.ws, "customer_id": c.ID, "name": c.Name, "domain": c.Domain, "stage": c.Stage,
		"contract_status": c.ContractStatus, "created": closeTimes(c.CreatedAt, c.UpdatedAt),
	}
	return sdk.Event{
		ID: "customer:" + c.ID + "@" + c.UpdatedAt, Type: TypeCustomer, TS: stamp(c.UpdatedAt),
		Room: &sdk.Room{ID: m.ws, Name: m.ws, Kind: "workspace"}, Sender: m.sender(cmp.Or(c.UpdatedByID, c.CreatedByID)),
		Text: "Customer updated: " + short(c.Name, 80), Data: encode(d),
	}
}

// closeTimes reports two Plane times less than a second apart.
func closeTimes(a, b string) bool {
	ta, err1 := time.Parse(time.RFC3339Nano, a)
	tb, err2 := time.Parse(time.RFC3339Nano, b)
	return err1 == nil && err2 == nil && tb.Sub(ta).Abs() < time.Second
}

func stamp(ts string) string {
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return sdk.FormatTime(t)
	}
	return sdk.FormatTime(time.Now())
}

func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nilIfEmpty(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

func encode(v any) json.RawMessage {
	b, _ := json.Marshal(v) //nolint:errcheck // plain maps always encode
	return b
}
