package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

// Page is one decoded list response. It covers the v2 offset and cursor
// envelopes and the v1 cursor envelope.
type Page struct {
	Items      []json.RawMessage
	NextOffset *int
	NextCursor string
	HasMore    bool
	TotalCount *int
	Paginated  bool
}

type envelope struct {
	Data       []json.RawMessage `json:"data"`
	Results    []json.RawMessage `json:"results"`
	Next       *int              `json:"next"`
	NextCursor *string           `json:"next_cursor"`
	HasMore    *bool             `json:"has_more"`
	NextPage   *bool             `json:"next_page_results"`
	TotalCount *int              `json:"total_count"`
	Pagination *struct {
		Style string `json:"style"`
	} `json:"pagination"`
}

// ParsePage decodes a list envelope. Paginated is false when body is not one.
func ParsePage(body []byte) Page {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		var arr []json.RawMessage
		if json.Unmarshal(body, &arr) == nil {
			return Page{Items: arr}
		}
		return Page{}
	}
	p := Page{TotalCount: env.TotalCount}
	switch {
	case env.Pagination != nil:
		p.Paginated = true
		p.Items = env.Data
		if env.Pagination.Style == "cursor" {
			if env.NextCursor != nil {
				p.NextCursor = *env.NextCursor
			}
			p.HasMore = env.HasMore != nil && *env.HasMore && p.NextCursor != ""
		} else {
			p.NextOffset = env.Next
			p.HasMore = env.Next != nil
		}
	case env.Results != nil:
		p.Paginated = true
		p.Items = env.Results
		if env.NextCursor != nil {
			p.NextCursor = *env.NextCursor
		}
		p.HasMore = env.NextPage != nil && *env.NextPage && p.NextCursor != ""
	}
	return p
}

// ListAll follows pagination and returns every item, up to max (0 = no cap).
// For v2 it asks for cursor pagination and falls back to offset when the
// ordering cannot use a cursor.
func (c *Client) ListAll(ctx context.Context, path string, q url.Values, v2 bool, max int) ([]json.RawMessage, error) {
	q = cloneValues(q)
	if q.Get("per_page") == "" {
		if v2 {
			q.Set("per_page", "200")
		} else {
			q.Set("per_page", "100")
		}
	}
	if v2 && q.Get("paginate") == "" && q.Get("cursor") == "" && q.Get("offset") == "" {
		q.Set("paginate", "cursor")
	}
	var all []json.RawMessage
	for {
		resp, err := c.Do(ctx, Request{Method: http.MethodGet, Path: path, Query: q})
		var apiErr *APIError
		if errors.As(err, &apiErr) && q.Get("paginate") == "cursor" && apiErr.StatusCode == http.StatusBadRequest {
			q.Del("paginate")
			continue
		}
		if err != nil {
			return nil, err
		}
		p := ParsePage(resp.Body)
		all = append(all, p.Items...)
		if max > 0 && len(all) >= max {
			return all[:max], nil
		}
		if !p.Paginated || !p.HasMore || len(p.Items) == 0 {
			return all, nil
		}
		if p.NextOffset != nil {
			q.Set("offset", strconv.Itoa(*p.NextOffset))
		} else {
			q.Del("paginate")
			q.Set("cursor", p.NextCursor)
		}
	}
}

func cloneValues(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}
