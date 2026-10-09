package serve

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/linear-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// delivery is the body of one Linear webhook request.
type delivery struct {
	Action    string          `json:"action"`
	Type      string          `json:"type"`
	CreatedAt string          `json:"createdAt"`
	Data      json.RawMessage `json:"data"`
	Actor     *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"actor"`
}

// ReceiveWebhook checks the signature with each stored secret and sends
// one issue or comment event. Other types give no event.
func (a *Adapter) ReceiveWebhook(ctx context.Context, req sdk.WebhookRequest) error {
	if req.Handshake {
		return nil
	}
	a.mu.Lock()
	secrets := make([]string, 0, len(a.st.Hooks)+1)
	for _, h := range a.st.Hooks {
		secrets = append(secrets, h.Secret)
	}
	if a.hook != nil {
		secrets = append(secrets, a.hook.Secret)
	}
	a.mu.Unlock()
	ok := false
	for _, s := range secrets {
		if req.VerifyHMAC([]byte(s), "linear-signature", "", sha256.New, sdk.EncodingHex) {
			ok = true
			break
		}
	}
	if !ok {
		return sdk.Invalid("the linear-signature header does not match the body")
	}
	var d delivery
	if err := json.Unmarshal([]byte(req.Body), &d); err != nil {
		return sdk.Invalid("the body is not a Linear delivery: " + err.Error())
	}
	var sender *sdk.Author
	if d.Actor != nil {
		sender = &sdk.Author{ID: d.Actor.ID, Name: d.Actor.Name, Bot: d.Actor.Type != "" && d.Actor.Type != "user"}
	}
	id := cmp.Or(req.Header("linear-delivery"), req.ID)
	var ev sdk.Event
	switch d.Type {
	case "Issue":
		var is client.Issue
		if err := json.Unmarshal(d.Data, &is); err != nil {
			return sdk.Invalid("issue data: " + err.Error())
		}
		ev = a.issueEvent(id, d.Action, is, sender, d.CreatedAt)
	case "Comment":
		var c client.Comment
		if err := json.Unmarshal(d.Data, &c); err != nil {
			return sdk.Invalid("comment data: " + err.Error())
		}
		ev = a.commentEvent(id, d.Action, c, sender, d.CreatedAt)
	default:
		return nil
	}
	e := sdk.EmitterFrom(ctx)
	if e == nil {
		return sdk.Permanent("no session")
	}
	return e.Event(ev)
}

// teamRoom is the room of a team. The name comes from the state when the
// payload has none.
func (a *Adapter) teamRoom(t *client.Team) *sdk.Room {
	if t == nil || t.Key == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return &sdk.Room{ID: t.Key, Name: cmp.Or(a.st.Teams[t.Key].Name, t.Name)}
}

func (a *Adapter) issueEvent(id, action string, is client.Issue, sender *sdk.Author, ts string) sdk.Event {
	room := a.teamRoom(is.Team)
	data := map[string]any{"action": action, "id": is.ID, "identifier": is.Identifier, "title": is.Title, "url": is.URL}
	if is.State != nil {
		data["state"] = is.State.Name
	}
	if room != nil {
		data["team"] = map[string]string{"key": room.ID, "name": room.Name}
	}
	raw, _ := json.Marshal(data) //nolint:errcheck // plain maps always encode
	return sdk.Event{ID: id, Type: "issue", TS: stamp(cmp.Or(ts, is.UpdatedAt)), Room: room, Sender: sender, Text: is.Identifier + " " + is.Title, Data: raw}
}

func (a *Adapter) commentEvent(id, action string, c client.Comment, sender *sdk.Author, ts string) sdk.Event {
	var room *sdk.Room
	data := map[string]any{"action": action, "id": c.ID, "body": c.Body, "url": c.URL}
	if c.Issue != nil {
		room = a.teamRoom(c.Issue.Team)
		data["issue"] = map[string]string{"id": c.Issue.ID, "identifier": c.Issue.Identifier, "title": c.Issue.Title}
	}
	raw, _ := json.Marshal(data) //nolint:errcheck // plain maps always encode
	return sdk.Event{ID: id, Type: "comment", TS: stamp(cmp.Or(ts, c.UpdatedAt)), Room: room, Sender: sender, Text: c.Body, Data: raw}
}

// stamp writes a Linear time as RFC 3339, or now when it has none.
func stamp(ts string) string {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return time.Now().UTC().Format(time.RFC3339)
}

// Run polls the issues and comments of the polled subscriptions. It stops
// when ctx ends.
func (a *Adapter) Run(ctx context.Context, e sdk.Emitter) error {
	t := time.NewTicker(a.interval)
	defer t.Stop()
	for {
		if err := a.poll(ctx, e); err != nil {
			a.log.WarnContext(ctx, "poll", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// poll sends the changes after the cursor. The first poll only sets the
// cursor, so a new subscription does not send old changes.
func (a *Adapter) poll(ctx context.Context, e sdk.Emitter) error {
	a.mu.Lock()
	watches := make([]watch, 0, len(a.st.Polls))
	for _, w := range a.st.Polls {
		watches = append(watches, w)
	}
	cursor := a.st.Cursor
	if len(watches) == 0 || cursor == "" {
		a.st.Cursor = time.Now().UTC().Format(timeLayout)
		err := a.save()
		a.mu.Unlock()
		return err
	}
	a.mu.Unlock()
	wants := func(team *client.Team, resource string) bool {
		key := ""
		if team != nil {
			key = team.Key
		}
		for _, w := range watches {
			if w.wants(key, resource) {
				return true
			}
		}
		return false
	}
	next := cursor
	issues, err := a.api.Issues(ctx, cursor, pollLimit)
	if err != nil {
		return err
	}
	for _, is := range issues {
		next = max(next, is.UpdatedAt)
		if !wants(is.Team, "Issue") {
			continue
		}
		var sender *sdk.Author
		if is.Creator != nil {
			sender = &sdk.Author{ID: is.Creator.ID, Name: is.Creator.Name}
		}
		action := "update"
		if is.CreatedAt == is.UpdatedAt {
			action = "create"
		}
		if err := e.Event(a.issueEvent("poll:issue:"+is.ID+":"+is.UpdatedAt, action, is, sender, is.UpdatedAt)); err != nil {
			return err
		}
	}
	comments, err := a.api.Comments(ctx, cursor, pollLimit)
	if err != nil {
		return err
	}
	for _, c := range comments {
		next = max(next, c.UpdatedAt)
		var team *client.Team
		if c.Issue != nil {
			team = c.Issue.Team
		}
		if !wants(team, "Comment") {
			continue
		}
		var sender *sdk.Author
		if c.User != nil {
			sender = &sdk.Author{ID: c.User.ID, Name: c.User.Name}
		}
		action := "update"
		if c.CreatedAt == c.UpdatedAt {
			action = "create"
		}
		if err := e.Event(a.commentEvent("poll:comment:"+c.ID+":"+c.UpdatedAt, action, c, sender, c.UpdatedAt)); err != nil {
			return err
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.st.Cursor = next
	return a.save()
}
