package listen

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/client"
)

// Poller finds changed work items: each interval it lists the items with
// updated_at at or after the last seen value. It cannot see deletes,
// archives, comments, or other resources.
type Poller struct {
	cl         *client.Client
	projects   *Projects
	out        Sink
	filter     Filter
	workspace  string
	listPath   string
	query      url.Values
	Interval   time.Duration
	Since      time.Time // zero: start from the last change
	Activities bool      // add the activity rows of each change

	cursor   string
	seen     map[string]map[string]any
	seenActs map[string]bool
}

// NewPoller lists listPath (a v2 work item list) with the extra query q.
func NewPoller(cl *client.Client, projects *Projects, out Sink, filter Filter, workspace, listPath string, q url.Values) *Poller {
	return &Poller{
		cl: cl, projects: projects, out: out, filter: filter,
		workspace: workspace, listPath: listPath, query: q,
		Interval: 15 * time.Second,
		seen:     map[string]map[string]any{},
		seenActs: map[string]bool{},
	}
}

// Run polls until ctx ends.
func (p *Poller) Run(ctx context.Context) error {
	if err := p.Start(ctx); err != nil {
		return err
	}
	t := time.NewTicker(p.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := p.Poll(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

// Start sets the cursor. Without Since it uses the newest updated_at, so that
// the server clock sets the start and old changes do not show.
func (p *Poller) Start(ctx context.Context) error {
	if !p.Since.IsZero() {
		p.cursor = p.Since.UTC().Format("2006-01-02T15:04:05.000000Z07:00") // the Plane layout, so that cursors compare as text
		return p.Poll(ctx)
	}
	q := cloneQuery(p.query)
	q.Set("order_by", "-updated_at")
	q.Set("per_page", "1")
	var page struct {
		Data []map[string]any `json:"data"`
	}
	if err := p.cl.GetJSON(ctx, p.listPath, q, &page); err != nil {
		return err
	}
	p.cursor = time.Now().UTC().Format("2006-01-02T15:04:05.000000Z07:00")
	if len(page.Data) > 0 {
		item := page.Data[0]
		p.cursor, _ = item["updated_at"].(string)
		p.seen[fmt.Sprint(item["id"])] = item
		if p.Activities {
			// Mark the activities of this change as seen.
			if _, err := p.activities(ctx, item, p.cursor); err != nil {
				return err
			}
		}
	}
	return nil
}

// Cursor is the newest updated_at that the poller saw.
func (p *Poller) Cursor() string { return p.cursor }

// Poll gets the changes after the cursor and writes one event for each.
func (p *Poller) Poll(ctx context.Context) error {
	q := cloneQuery(p.query)
	q.Set("updated_at__gte", p.cursor)
	q.Set("order_by", "updated_at")
	items, err := p.cl.ListAll(ctx, p.listPath, q, true, 0)
	if err != nil {
		return err
	}
	start := p.cursor
	for _, raw := range items {
		var item map[string]any
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		id := fmt.Sprint(item["id"])
		updated, _ := item["updated_at"].(string)
		prev := p.seen[id]
		if prev != nil && prev["updated_at"] == updated {
			continue
		}
		// Mark the item as seen only after the emit, so a failed change comes again on the next poll.
		mark := func() {
			p.seen[id] = item
			if updated > p.cursor {
				p.cursor = updated
			}
		}
		ev := Event{
			Source:     "poll",
			Event:      pollEventName(prev, item),
			EntityType: "issue",
			EntityID:   id,
			EventID:    id + "@" + updated,
			Data:       item,
		}
		if !p.filter.Match(ev.Event) {
			mark()
			continue
		}
		ev.Key = p.projects.workItemKey(ctx, item)
		if prev != nil {
			ev.PreviousAttributes = changed(prev, item)
		}
		if p.Activities {
			since := start
			if prev != nil {
				since, _ = prev["updated_at"].(string)
			}
			if ev.Activities, err = p.activities(ctx, item, since); err != nil {
				return err
			}
		}
		if err := p.out.Emit(ev); err != nil {
			return err
		}
		mark()
	}
	return nil
}

func (p *Poller) activities(ctx context.Context, item map[string]any, since string) ([]json.RawMessage, error) {
	path := fmt.Sprintf("/api/v2/workspaces/%s/projects/%s/work-items/%s/activities/",
		url.PathEscape(p.workspace), url.PathEscape(fmt.Sprint(item["project_id"])), url.PathEscape(fmt.Sprint(item["id"])))
	q := url.Values{"created_at__gte": {since}, "order_by": {"created_at"}}
	items, err := p.cl.ListAll(ctx, path, q, true, 0)
	if err != nil {
		return nil, err
	}
	// An activity can be a little newer than updated_at, so the next poll sees it again.
	var out []json.RawMessage
	for _, raw := range items {
		var a struct{ ID string }
		if json.Unmarshal(raw, &a) == nil && a.ID != "" {
			if p.seenActs[a.ID] {
				continue
			}
			p.seenActs[a.ID] = true
		}
		out = append(out, raw)
	}
	return out, nil
}

// pollEventName names a change like the webhook does. A new item has
// created_at and updated_at within a second.
func pollEventName(prev, item map[string]any) string {
	if prev == nil {
		c, err1 := time.Parse(time.RFC3339Nano, fmt.Sprint(item["created_at"]))
		u, err2 := time.Parse(time.RFC3339Nano, fmt.Sprint(item["updated_at"]))
		if err1 == nil && err2 == nil && u.Sub(c) < time.Second {
			return "workitem.created"
		}
	}
	return "workitem.updated"
}

// changed returns the previous value of each field that differs.
func changed(prev, cur map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range prev {
		if k == "updated_at" {
			continue
		}
		if !reflect.DeepEqual(v, cur[k]) {
			out[k] = v
		}
	}
	for k := range cur {
		if _, ok := prev[k]; !ok {
			out[k] = nil
		}
	}
	return out
}

func cloneQuery(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}
