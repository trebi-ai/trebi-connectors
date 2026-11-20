// Package listen gets Plane change events, by polling or through a webhook,
// and writes them as JSON lines.
package listen

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/client"
)

// Event has the shape of a Plane v2 webhook payload, plus the source and the
// work item key. Poll events have no delivery or webhook ids.
type Event struct {
	Source             string            `json:"source"`
	Event              string            `json:"event"`
	EntityType         string            `json:"entity_type,omitempty"`
	EntityID           string            `json:"entity_id,omitempty"`
	Key                string            `json:"key,omitempty"`
	EventID            string            `json:"event_id,omitempty"`
	DeliveryID         string            `json:"delivery_id,omitempty"`
	WebhookID          string            `json:"webhook_id,omitempty"`
	WorkspaceID        string            `json:"workspace_id,omitempty"`
	Data               map[string]any    `json:"data"`
	PreviousAttributes map[string]any    `json:"previous_attributes,omitempty"`
	Activities         []json.RawMessage `json:"activities,omitempty"`
}

// Filter matches event names against glob patterns such as "workitem.*".
// An empty filter matches all events.
type Filter []string

// ParseFilter splits comma-separated patterns and checks their syntax.
func ParseFilter(s string) (Filter, error) {
	var f Filter
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("bad event pattern %q: %w", p, err)
		}
		f = append(f, p)
	}
	return f, nil
}

// Match reports whether name matches one of the patterns.
func (f Filter) Match(name string) bool {
	if len(f) == 0 {
		return true
	}
	for _, p := range f {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// Scopes returns the webhook scopes that the filter selects.
func (f Filter) Scopes() []string {
	var out []string
	for _, s := range WebhookScopes {
		if f.Match(s) {
			out = append(out, s)
		}
	}
	return out
}

// WebhookScopes are the v2 webhook events (developers.plane.so/dev-tools/intro-webhooks).
var WebhookScopes = []string{
	"project.created", "project.updated", "project.archived", "project.deleted",
	"cycle.created", "cycle.updated", "cycle.archived", "cycle.deleted",
	"module.created", "module.updated", "module.archived", "module.deleted",
	"milestone.created", "milestone.updated", "milestone.deleted",
	"page.created", "page.updated", "page.archived", "page.deleted",
	"page.comment.created", "page.comment.updated", "page.comment.deleted",
	"workitem.created", "workitem.updated", "workitem.archived", "workitem.deleted",
	"workitem.comment.created", "workitem.comment.updated", "workitem.comment.deleted",
	"workitem.link.created", "workitem.link.updated", "workitem.link.deleted",
	"workitem.vote.created", "workitem.vote.deleted",
	"workitem.attachment.created", "workitem.attachment.updated", "workitem.attachment.deleted",
	"workitem.relation.created", "workitem.relation.deleted",
	"workitem.dependency.created", "workitem.dependency.deleted",
	"workitem.page_link.created", "workitem.page_link.deleted",
}

// Sink takes the events of a poller or a receiver.
type Sink interface {
	Emit(ev Event) error
}

// Emitter writes events as JSON lines. It is safe for concurrent use.
type Emitter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

// NewEmitter writes to w.
func NewEmitter(w io.Writer) *Emitter {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &Emitter{enc: enc}
}

// Emit writes one event.
func (e *Emitter) Emit(ev Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enc.Encode(ev)
}

// Projects maps project UUIDs to identifiers (for example "ENG"), to make
// work item keys. It loads the project list once.
type Projects struct {
	cl        *client.Client
	workspace string
	once      sync.Once
	ids       map[string]string
	err       error
}

// NewProjects reads the projects of workspace with cl.
func NewProjects(cl *client.Client, workspace string) *Projects {
	return &Projects{cl: cl, workspace: workspace}
}

// Identifier returns the identifier of the project with the given UUID.
func (p *Projects) Identifier(ctx context.Context, id string) string {
	p.once.Do(func() {
		p.ids = map[string]string{}
		items, err := p.cl.ListAll(ctx, fmt.Sprintf("/api/v2/workspaces/%s/projects/", url.PathEscape(p.workspace)), url.Values{"fields": {"id,identifier"}}, true, 0)
		if err != nil {
			p.err = err
			return
		}
		for _, raw := range items {
			var pr struct{ ID, Identifier string }
			if json.Unmarshal(raw, &pr) == nil {
				p.ids[pr.ID] = pr.Identifier
			}
		}
	})
	return p.ids[id]
}

// workItemKey returns "ENG-42" for a work item object, or "".
func (p *Projects) workItemKey(ctx context.Context, data map[string]any) string {
	if k, ok := data["identifier"].(string); ok && strings.Contains(k, "-") {
		return k
	}
	pid, _ := data["project_id"].(string)
	seq, ok := data["sequence_id"].(float64)
	if pid == "" || !ok {
		return ""
	}
	if ident := p.Identifier(ctx, pid); ident != "" {
		return fmt.Sprintf("%s-%d", ident, int(seq))
	}
	return ""
}
