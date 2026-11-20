package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ClosedGroup reports whether a state group closes a work item.
func ClosedGroup(g string) bool { return g == "completed" || g == "cancelled" }

// StateGroup returns the group of a project state, by id or by name.
// It returns "" when the project has no such state.
func (c *Client) StateGroup(ctx context.Context, workspace, project, ref string) (string, error) {
	path := fmt.Sprintf("/api/v2/workspaces/%s/projects/%s/states/", url.PathEscape(workspace), url.PathEscape(project))
	rows, err := c.ListAll(ctx, path, url.Values{"fields": {"id,name,group"}}, true, 0)
	if err != nil {
		return "", fmt.Errorf("list states: %w", err)
	}
	for _, r := range rows {
		var s struct{ ID, Name, Group string }
		if json.Unmarshal(r, &s) == nil && (s.ID == ref || strings.EqualFold(s.Name, ref)) {
			return s.Group, nil
		}
	}
	return "", nil
}

// subItem is one row of the sub-item walk.
type subItem struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Seq       int    `json:"sequence_id"`
	State     *struct {
		Group string `json:"group"`
	} `json:"state"`
}

// OpenSubItems returns the key of a work item and the keys of its open
// sub-items at all levels.
func (c *Client) OpenSubItems(ctx context.Context, workspace, ref string) (string, []string, error) {
	ws := url.PathEscape(workspace)
	var root subItem
	if err := c.GetJSON(ctx, fmt.Sprintf("/api/v2/workspaces/%s/work-items/%s/", ws, url.PathEscape(ref)),
		url.Values{"fields": {"id,project_id,sequence_id"}}, &root); err != nil {
		return "", nil, fmt.Errorf("get work item %q: %w", ref, err)
	}
	idents := map[string]string{}
	key := func(it subItem) (string, error) {
		if _, ok := idents[it.ProjectID]; !ok {
			var p struct{ Identifier string }
			path := fmt.Sprintf("/api/v2/workspaces/%s/projects/%s/", ws, url.PathEscape(it.ProjectID))
			if err := c.GetJSON(ctx, path, url.Values{"fields": {"identifier"}}, &p); err != nil {
				return "", fmt.Errorf("get project %s: %w", it.ProjectID, err)
			}
			idents[it.ProjectID] = p.Identifier
		}
		return fmt.Sprintf("%s-%d", idents[it.ProjectID], it.Seq), nil
	}
	rootKey, err := key(root)
	if err != nil {
		return "", nil, err
	}
	var open []string
	seen := map[string]bool{root.ID: true}
	for level := []string{root.ID}; len(level) > 0; {
		rows, err := c.ListAll(ctx, fmt.Sprintf("/api/v2/workspaces/%s/work-items/", ws), url.Values{
			"parent_id__in": {strings.Join(level, ",")},
			"fields":        {"id,project_id,sequence_id,state_id"},
			"expand":        {"state"},
		}, true, 0)
		if err != nil {
			return "", nil, fmt.Errorf("list sub-items: %w", err)
		}
		level = nil
		for _, r := range rows {
			var it subItem
			if err := json.Unmarshal(r, &it); err != nil || seen[it.ID] {
				continue
			}
			seen[it.ID] = true
			level = append(level, it.ID)
			if it.State != nil && ClosedGroup(it.State.Group) {
				continue
			}
			k, err := key(it)
			if err != nil {
				return "", nil, err
			}
			open = append(open, k)
		}
	}
	return rootKey, open, nil
}
