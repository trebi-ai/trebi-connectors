package client

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
)

var (
	uuidRE  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	identRE = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*)-([0-9]+)$`)
)

// IsUUID reports whether s is a UUID.
func IsUUID(s string) bool { return uuidRE.MatchString(s) }

// SplitIdentifier splits a work item key like "ENG-42" into project identifier
// and sequence number.
func SplitIdentifier(s string) (project, seq string, ok bool) {
	m := identRE.FindStringSubmatch(s)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// ProjectID resolves a project identifier (for example "ENG") to its UUID.
// v2 paths accept either form; v1 paths need the UUID.
func (c *Client) ProjectID(ctx context.Context, workspace, ref string) (string, error) {
	if IsUUID(ref) {
		return ref, nil
	}
	if id, ok := c.projectIDs[ref]; ok {
		return id, nil
	}
	var p struct {
		ID string `json:"id"`
	}
	path := fmt.Sprintf("/api/v2/workspaces/%s/projects/%s/", url.PathEscape(workspace), url.PathEscape(ref))
	if err := c.GetJSON(ctx, path, url.Values{"fields": {"id"}}, &p); err != nil {
		return "", fmt.Errorf("resolve project %q: %w", ref, err)
	}
	c.projectIDs[ref] = p.ID
	return p.ID, nil
}

// WorkItemID resolves a work item key like "ENG-42" to its UUID.
func (c *Client) WorkItemID(ctx context.Context, workspace, ref string) (string, error) {
	if IsUUID(ref) {
		return ref, nil
	}
	var w struct {
		ID string `json:"id"`
	}
	path := fmt.Sprintf("/api/v2/workspaces/%s/work-items/%s/", url.PathEscape(workspace), url.PathEscape(ref))
	if err := c.GetJSON(ctx, path, url.Values{"fields": {"id"}}, &w); err != nil {
		return "", fmt.Errorf("resolve work item %q: %w", ref, err)
	}
	return w.ID, nil
}
