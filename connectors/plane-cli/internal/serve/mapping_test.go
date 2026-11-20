package serve

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

var (
	testProject = project{ID: "p1", Identifier: "ENG", Name: "Engineering", Intake: true}
	testMapper  = mapper{ws: "acme", web: "https://app.plane.so", members: map[string]member{
		"u1": {Name: "ana", Email: "ana@example.com"},
		"b1": {Name: "Intake", Email: "acme-intake@plane.so"},
	}}
)

func strp(s string) *string { return &s }

func testItem() workItem {
	return workItem{
		ID: "w1", Identifier: "ENG-7", Name: "Fix login", ProjectID: "p1", SequenceID: 7, StateID: "s2",
		State: &state{ID: "s2", Name: "In Progress", Group: "started"}, Labels: []label{{ID: "l1", Name: "agent"}},
		Priority: "high", CreatedAt: "2026-10-01T10:00:00.000000Z", UpdatedAt: "2026-10-02T10:00:00.000000Z",
		CreatedByID: "u1", UpdatedByID: "u1",
	}
}

func prevSnap() *snap {
	s := testItem().snapshot()
	s.U, s.StateID, s.State, s.Group = "2026-10-01T10:00:00.000000Z", "s1", "Todo", "unstarted"
	return &s
}

func data(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func types(evs []sdk.Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func TestWorkItemCreated(t *testing.T) {
	it := testItem()
	it.UpdatedAt = it.CreatedAt
	evs := testMapper.workItemEvents(testProject, it, nil, nil, true)
	if len(evs) != 1 || evs[0].Type != TypeCreated {
		t.Fatalf("types = %v", types(evs))
	}
	e := evs[0]
	if e.Room.ID != "acme/ENG" || e.Sender.Name != "ana" || e.Sender.Bot {
		t.Errorf("room %+v sender %+v", e.Room, e.Sender)
	}
	if e.Text != "Created ENG-7 Fix login" {
		t.Errorf("text = %q", e.Text)
	}
	d := data(t, e.Data)
	if d["identifier"] != "ENG-7" || d["work_item_id"] != "w1" || d["state_group"] != "started" || d["url"] != "https://app.plane.so/acme/browse/ENG-7/" {
		t.Errorf("data = %v", d)
	}
	if ls, _ := d["labels"].([]any); len(ls) != 1 || ls[0] != "agent" {
		t.Errorf("labels = %v", d["labels"])
	}
}

func TestWorkItemStateFromSnapshot(t *testing.T) {
	evs := testMapper.workItemEvents(testProject, testItem(), prevSnap(), nil, false)
	if len(evs) != 1 || evs[0].Type != TypeStateChanged {
		t.Fatalf("types = %v", types(evs))
	}
	d := data(t, evs[0].Data)
	if d["field"] != "state" || d["old"] != "Todo" || d["new"] != "In Progress" || d["old_state_group"] != "unstarted" || d["state_group"] != "started" {
		t.Errorf("data = %v", d)
	}
	if evs[0].Text != "ENG-7 Fix login: Todo → In Progress" {
		t.Errorf("text = %q", evs[0].Text)
	}
}

func TestWorkItemStateFromActivity(t *testing.T) {
	prev := testItem().snapshot()
	prev.U = "2026-10-01T10:00:00.000000Z"
	prev.StateID = "" // an old snapshot without the state id
	acts := []activity{{ID: "a1", ActorID: "u1", Field: strp("state"), OldValue: strp("Backlog"), NewValue: strp("In Progress")}}
	evs := testMapper.workItemEvents(testProject, testItem(), &prev, acts, false)
	if len(evs) != 1 || evs[0].Type != TypeStateChanged {
		t.Fatalf("types = %v", types(evs))
	}
	if d := data(t, evs[0].Data); d["old"] != "Backlog" {
		t.Errorf("old = %v", d["old"])
	}
}

func TestWorkItemUpdatedFromActivity(t *testing.T) {
	prev := testItem().snapshot()
	prev.U, prev.Priority = "2026-10-01T10:00:00.000000Z", "low"
	acts := []activity{{ID: "a1", ActorID: "u1", Field: strp("priority"), OldValue: strp("low"), NewValue: strp("high")}}
	evs := testMapper.workItemEvents(testProject, testItem(), &prev, acts, false)
	if len(evs) != 1 || evs[0].Type != TypeUpdated {
		t.Fatalf("types = %v", types(evs))
	}
	d := data(t, evs[0].Data)
	if d["field"] != "priority" || d["old"] != "low" || d["new"] != "high" {
		t.Errorf("data = %v", d)
	}
	if evs[0].Text != "ENG-7 Fix login: priority low → high" {
		t.Errorf("text = %q", evs[0].Text)
	}
}

func TestWorkItemLabelFromSnapshot(t *testing.T) {
	prev := testItem().snapshot()
	prev.U, prev.Labels = "2026-10-01T10:00:00.000000Z", nil
	evs := testMapper.workItemEvents(testProject, testItem(), &prev, nil, false)
	if len(evs) != 1 || evs[0].Type != TypeUpdated {
		t.Fatalf("types = %v", types(evs))
	}
	d := data(t, evs[0].Data)
	if d["field"] != "labels" {
		t.Errorf("data = %v", d)
	}
	if n, _ := d["new"].([]any); len(n) != 1 || n[0] != "agent" {
		t.Errorf("new = %v", d["new"])
	}
}

func TestWorkItemStateAndLabel(t *testing.T) {
	prev := prevSnap()
	prev.Labels = nil
	evs := testMapper.workItemEvents(testProject, testItem(), prev, nil, false)
	if got := strings.Join(types(evs), ","); got != TypeStateChanged+","+TypeUpdated {
		t.Fatalf("types = %s", got)
	}
	if evs[0].ID == evs[1].ID {
		t.Errorf("the two events have one id %s", evs[0].ID)
	}
}

func TestWorkItemUnknownChange(t *testing.T) {
	prev := testItem().snapshot()
	prev.U = "2026-10-01T10:00:00.000000Z"
	acts := []activity{{ID: "a1", ActorID: "u1", Field: strp("description"), OldValue: strp("old text"), NewValue: strp("new text")}}
	evs := testMapper.workItemEvents(testProject, testItem(), &prev, acts, false)
	if len(evs) != 1 || evs[0].Type != TypeUpdated || evs[0].Text != "ENG-7 Fix login: description changed" {
		t.Fatalf("events = %+v", evs)
	}
	evs = testMapper.workItemEvents(testProject, testItem(), &prev, nil, false)
	if len(evs) != 1 || evs[0].Type != TypeUpdated || evs[0].Text != "ENG-7 Fix login changed" {
		t.Fatalf("events = %+v", evs)
	}
}

func TestIntakeTransitions(t *testing.T) {
	in := intakeItem{ID: "i1", Name: "Login fails", WorkItemID: "w1", Status: IntakePending, UpdatedAt: "2026-10-02T10:00:00.000000Z", CreatedByID: "b1"}
	e, ok := testMapper.intakeEvent(testProject, in, nil, "ENG-7")
	if !ok || e.Type != TypeIntakeCreated || !e.Sender.Bot || e.Text != "New intake item in ENG: ENG-7 Login fails" {
		t.Fatalf("created = %+v", e)
	}
	cases := []struct {
		from, to int
		want     string
	}{
		{IntakePending, IntakeAccepted, TypeIntakeAccepted},
		{IntakePending, IntakeDeclined, TypeIntakeDeclined},
		{IntakePending, IntakeDuplicate, TypeIntakeDup},
		{IntakePending, IntakeSnoozed, TypeIntakeSnoozed},
		{IntakeSnoozed, IntakeAccepted, TypeIntakeAccepted},
		{IntakeSnoozed, IntakePending, ""},
		{IntakeDeclined, IntakePending, ""},
	}
	for _, c := range cases {
		in.Status, in.UpdatedByID = c.to, "u1"
		from := c.from
		e, ok := testMapper.intakeEvent(testProject, in, &from, "ENG-7")
		if c.want == "" {
			if ok {
				t.Errorf("%d → %d: event %s, want none", c.from, c.to, e.Type)
			}
			continue
		}
		if !ok || e.Type != c.want {
			t.Errorf("%d → %d: type %q, want %q", c.from, c.to, e.Type, c.want)
			continue
		}
		d := data(t, e.Data)
		if d["old"] != IntakeStatus(c.from) || d["new"] != IntakeStatus(c.to) || d["work_item_id"] != "w1" || e.Sender.Name != "ana" {
			t.Errorf("%d → %d: data %v sender %+v", c.from, c.to, d, e.Sender)
		}
		if !strings.HasSuffix(e.ID, ":"+IntakeStatus(c.to)) {
			t.Errorf("id = %s", e.ID)
		}
	}
}

func TestCommentEvent(t *testing.T) {
	s := testItem().snapshot()
	c := comment{ID: "c1", Stripped: "  Please check  ", ActorID: "u1", CreatedAt: "2026-10-02T11:00:00.000000Z"}
	e := testMapper.commentEvent(testProject, s, "w1", "ENG-7", c)
	if e.Type != TypeComment || e.ID != "comment:c1" || e.Text != "ENG-7: Please check" || e.Room.ID != "acme/ENG" {
		t.Fatalf("event = %+v", e)
	}
	d := data(t, e.Data)
	if d["comment"] != "Please check" || d["state_group"] != "started" || d["work_item_id"] != "w1" {
		t.Errorf("data = %v", d)
	}
}

func TestCustomerEvent(t *testing.T) {
	c := customer{ID: "k1", Name: "Acme Inc", Stage: "lead", CreatedAt: "2026-10-02T11:00:00.000000Z", UpdatedAt: "2026-10-02T11:00:00.200000Z", UpdatedByID: "u1"}
	e := testMapper.customerEvent(c)
	if e.Type != TypeCustomer || e.Room.ID != "acme" || e.Text != "Customer updated: Acme Inc" {
		t.Fatalf("event = %+v", e)
	}
	if d := data(t, e.Data); d["created"] != true || d["stage"] != "lead" {
		t.Errorf("data = %v", d)
	}
}

func TestScope(t *testing.T) {
	sc, err := ParseScope("acme/*, zeta/eng zeta/WEB\nacme-labs")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sc.Workspaces(), ","); got != "acme,acme-labs,zeta" {
		t.Errorf("workspaces = %s", got)
	}
	if !sc.All("acme") || !sc.All("acme-labs") || sc.All("zeta") {
		t.Errorf("all = %v", sc)
	}
	if !sc.Has("zeta", "ENG") || !sc.Has("zeta", "web") || sc.Has("zeta", "OPS") || sc.Has("other", "X") || !sc.Has("acme", "DOCS") {
		t.Errorf("has = %v", sc)
	}
	sc, _ = ParseScope("zeta/ENG, zeta/*")
	if !sc.All("zeta") {
		t.Errorf("a later * must widen the scope: %v", sc)
	}
	for _, bad := range []string{"", " , ", "/ENG", "a/b/c"} {
		if _, err := ParseScope(bad); err == nil {
			t.Errorf("ParseScope(%q) has no error", bad)
		}
	}
}
