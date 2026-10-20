package client

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
)

// Page is the list part of a Web API response.
type Page struct {
	// Key is the name of the list field, for example "channels".
	Key        string
	Items      []json.RawMessage
	NextCursor string
}

// ParsePage finds the list field of a response and the next cursor. A
// response with more than one list uses the largest list.
func ParsePage(body []byte) (Page, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return Page{}, false
	}
	var p Page
	var meta struct {
		NextCursor string `json:"next_cursor"`
	}
	if raw, ok := m["response_metadata"]; ok {
		_ = json.Unmarshal(raw, &meta)
	}
	p.NextCursor = meta.NextCursor
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	found := false
	for _, k := range keys {
		var items []json.RawMessage
		if json.Unmarshal(m[k], &items) != nil {
			continue
		}
		if !found || len(items) > len(p.Items) {
			p.Key, p.Items, found = k, items, true
		}
	}
	return p, found
}

// ListAll calls a cursor-paginated method until the last page. max > 0
// stops after that many items.
func (c *Client) ListAll(ctx context.Context, method string, params url.Values, max int) (Page, error) {
	q := url.Values{}
	for k, v := range params {
		q[k] = v
	}
	if q.Get("limit") == "" {
		q.Set("limit", "200")
	}
	var all Page
	for {
		body, err := c.Call(ctx, method, q)
		if err != nil {
			return all, err
		}
		p, _ := ParsePage(body)
		if all.Key == "" {
			all.Key = p.Key
		}
		all.Items = append(all.Items, p.Items...)
		if max > 0 && len(all.Items) >= max {
			all.Items = all.Items[:max]
			return all, nil
		}
		if p.NextCursor == "" {
			return all, nil
		}
		q.Set("cursor", p.NextCursor)
	}
}
