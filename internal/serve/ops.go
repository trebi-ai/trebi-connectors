package serve

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// HistoryQuery selects the messages of one channel or thread.
type HistoryQuery struct {
	Channel string // channel id
	Thread  string // thread ts; empty reads the channel
	Before  string // ts; only older messages
	After   string // ts; only newer messages
	Limit   int
}

// Page is a list of messages, oldest first.
type Page struct {
	Messages []sdk.Event `json:"messages"`
	// Next is the ts to give as before for the older messages of a
	// channel, or as after for the newer replies of a thread.
	Next  string `json:"next,omitempty"`
	Total int    `json:"total,omitempty"`
}

// History reads the messages of a channel, or the replies of a thread.
func (t *Team) History(ctx context.Context, q HistoryQuery) (Page, error) {
	limit := min(max(q.Limit, 1), 200)
	if q.Limit == 0 {
		limit = 50
	}
	p := url.Values{"channel": {q.Channel}, "limit": {strconv.Itoa(limit)}}
	if q.Before != "" {
		p.Set("latest", q.Before)
	}
	if q.After != "" {
		p.Set("oldest", q.After)
	}
	method := "conversations.history"
	if q.Thread != "" {
		method = "conversations.replies"
		p.Set("ts", q.Thread)
	}
	var resp struct {
		Messages []json.RawMessage `json:"messages"`
		HasMore  bool              `json:"has_more"`
	}
	if err := t.Client.CallJSON(ctx, method, p, &resp); err != nil {
		return Page{}, err
	}
	if q.Thread == "" {
		slices.Reverse(resp.Messages) // history is newest first
	}
	page := Page{Messages: make([]sdk.Event, 0, len(resp.Messages))}
	var oldest, newest string
	for _, raw := range resp.Messages {
		var m message
		if json.Unmarshal(raw, &m) != nil || m.TS == "" {
			continue
		}
		oldest, newest = cmpOr(oldest, m.TS), m.TS
		page.Messages = append(page.Messages, t.message(ctx, q.Channel, m, raw))
	}
	if resp.HasMore {
		page.Next = oldest
		if q.Thread != "" {
			page.Next = newest
		}
	}
	return page, nil
}

// Search finds messages with the Slack search syntax, newest first.
func (t *Team) Search(ctx context.Context, query string, limit int) (Page, error) {
	if limit == 0 {
		limit = 20
	}
	limit = min(max(limit, 1), 100)
	p := url.Values{"query": {query}, "count": {strconv.Itoa(limit)}, "sort": {"timestamp"}, "sort_dir": {"desc"}}
	var resp struct {
		Messages struct {
			Total   int               `json:"total"`
			Matches []json.RawMessage `json:"matches"`
		} `json:"messages"`
	}
	if err := t.Client.CallJSON(ctx, "search.messages", p, &resp); err != nil {
		return Page{}, err
	}
	page := Page{Messages: make([]sdk.Event, 0, len(resp.Messages.Matches)), Total: resp.Messages.Total}
	for _, raw := range resp.Messages.Matches {
		var m message
		if json.Unmarshal(raw, &m) != nil || m.TS == "" || m.Channel == "" {
			continue
		}
		page.Messages = append(page.Messages, t.message(ctx, string(m.Channel), m, raw))
	}
	return page, nil
}
