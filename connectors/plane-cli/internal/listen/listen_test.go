package listen

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/config"
)

const projectID = "11111111-1111-1111-1111-111111111111"

func fakeAPI(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return client.New(&config.Config{APIKey: "k", Host: srv.URL})
}

func projectsHandler(w http.ResponseWriter, r *http.Request) bool {
	if strings.HasSuffix(r.URL.Path, "/projects/") {
		fmt.Fprintf(w, `{"data":[{"id":%q,"identifier":"ENG"}],"next_cursor":null,"has_more":false,"pagination":{"style":"cursor"}}`, projectID)
		return true
	}
	return false
}

func lines(t *testing.T, buf *bytes.Buffer) []Event {
	t.Helper()
	var out []Event
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
		out = append(out, ev)
	}
	return out
}

func TestFilter(t *testing.T) {
	f, err := ParseFilter("workitem.*, cycle.created")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"workitem.created": true, "workitem.comment.created": true,
		"cycle.created": true, "cycle.deleted": false, "project.created": false,
	} {
		if f.Match(name) != want {
			t.Errorf("Match(%q) != %v", name, want)
		}
	}
	if s := f.Scopes(); len(s) != 22 {
		t.Errorf("scopes = %d %v", len(s), s)
	}
	if len(Filter(nil).Scopes()) != len(WebhookScopes) {
		t.Errorf("empty filter does not select all scopes")
	}
	if _, err := ParseFilter("workitem.["); err == nil {
		t.Errorf("bad pattern accepted")
	}
}

func TestPoller(t *testing.T) {
	var mu sync.Mutex
	item := map[string]any{"id": "w1", "identifier": "ENG-1", "project_id": projectID, "priority": "none",
		"created_at": "2026-01-01T00:00:00.000000Z", "updated_at": "2026-01-01T00:00:00.000000Z"}
	var listed []map[string]any
	var gte []string
	cl := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if projectsHandler(w, r) {
			return
		}
		q := r.URL.Query()
		if q.Get("order_by") == "-updated_at" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{item}})
			return
		}
		gte = append(gte, q.Get("updated_at__gte"))
		_ = json.NewEncoder(w).Encode(map[string]any{"data": listed, "next_cursor": nil, "has_more": false, "pagination": map[string]string{"style": "cursor"}})
	})
	var buf bytes.Buffer
	p := NewPoller(cl, NewProjects(cl, "acme"), NewEmitter(&buf), nil, "acme", "/api/v2/workspaces/acme/work-items/", nil)
	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}

	updated := map[string]any{}
	for k, v := range item {
		updated[k] = v
	}
	updated["priority"], updated["updated_at"] = "high", "2026-01-01T00:05:00.000000Z"
	created := map[string]any{"id": "w2", "project_id": projectID, "sequence_id": 2.0,
		"created_at": "2026-01-01T00:06:00.000000Z", "updated_at": "2026-01-01T00:06:00.000010Z"}
	listed = []map[string]any{item, updated, created}
	if err := p.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	// The same rows again give no events.
	listed = []map[string]any{created}
	if err := p.Poll(ctx); err != nil {
		t.Fatal(err)
	}

	evs := lines(t, &buf)
	if len(evs) != 2 {
		t.Fatalf("events = %+v", evs)
	}
	if evs[0].Event != "workitem.updated" || evs[0].Key != "ENG-1" || evs[0].PreviousAttributes["priority"] != "none" {
		t.Errorf("update event = %+v", evs[0])
	}
	if evs[1].Event != "workitem.created" || evs[1].Key != "ENG-2" || evs[1].PreviousAttributes != nil {
		t.Errorf("create event = %+v", evs[1])
	}
	if gte[0] != "2026-01-01T00:00:00.000000Z" || gte[1] != "2026-01-01T00:06:00.000010Z" {
		t.Errorf("cursors = %v", gte)
	}
}

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func deliver(t *testing.T, r *Receiver, body, sig, event string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-Plane-Signature", sig)
	req.Header.Set("X-Plane-Event", event)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code
}

func TestReceiver(t *testing.T) {
	cl := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if !projectsHandler(w, r) {
			w.WriteHeader(404)
		}
	})
	var buf bytes.Buffer
	filter, _ := ParseFilter("workitem.*")
	r := NewReceiver(NewProjects(cl, "acme"), NewEmitter(&buf), filter)
	r.ProjectID = projectID

	created := fmt.Sprintf(`{"version":"v2","event_id":"e1","entity_id":"w1","entity_type":"issue","event":"WebhookScope.ScopeChoices.WORK_ITEM_CREATED","data":{"id":"w1","project_id":%q,"sequence_id":7},"previous_attributes":"{}"}`, projectID)
	if code := deliver(t, r, created, "x", "workitem.created"); code != http.StatusServiceUnavailable {
		t.Errorf("no secret: code %d", code)
	}
	r.SetSecret("plane_wh_s")
	if code := deliver(t, r, created, sign("other", []byte(created)), "workitem.created"); code != http.StatusUnauthorized {
		t.Errorf("bad signature: code %d", code)
	}
	for i := 0; i < 2; i++ { // the second delivery is a retry of e1
		if code := deliver(t, r, created, sign("plane_wh_s", []byte(created)), "workitem.created"); code != 200 {
			t.Errorf("good delivery: code %d", code)
		}
	}
	deleted := fmt.Sprintf(`{"event_id":"e2","entity_type":"issue","event":"workitem.deleted","data":"{}","previous_attributes":{"id":"w1","project_id":%q,"sequence_id":7}}`, projectID)
	deliver(t, r, deleted, sign("plane_wh_s", []byte(deleted)), "workitem.deleted")
	other := `{"event_id":"e3","event":"workitem.updated","data":{"project_id":"22222222-2222-2222-2222-222222222222"}}`
	deliver(t, r, other, sign("plane_wh_s", []byte(other)), "workitem.updated")
	cycle := `{"event_id":"e4","event":"cycle.created","data":{}}`
	deliver(t, r, cycle, sign("plane_wh_s", []byte(cycle)), "cycle.created")

	evs := lines(t, &buf)
	if len(evs) != 2 {
		t.Fatalf("events = %+v", evs)
	}
	if evs[0].Event != "workitem.created" || evs[0].Source != "webhook" || evs[0].Key != "ENG-7" || evs[0].PreviousAttributes != nil {
		t.Errorf("created = %+v", evs[0])
	}
	if evs[1].Event != "workitem.deleted" || evs[1].Data != nil || evs[1].Key != "ENG-7" {
		t.Errorf("deleted = %+v", evs[1])
	}
}
