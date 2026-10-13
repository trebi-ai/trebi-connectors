package serve

import (
	"context"
	"encoding/json"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/trebi-ai/trebi-connectors/sdk"

	"github.com/flarco/cli-tools/slack-cli/internal/client"
)

// Event types of the adapter.
const (
	TypeMessage = "message"
	TypeEdited  = "message.edited"
	TypeDeleted = "message.deleted"
	TypeReact   = "reaction"
	TypeMention = "mention"
	TypeJoin    = "channel.join"
)

// Events lists the event types for initialize.
var Events = []sdk.EventDecl{{Type: TypeMessage}, {Type: TypeEdited}, {Type: TypeDeleted}, {Type: TypeReact}, {Type: TypeMention}, {Type: TypeJoin}}

// Names looks up the users and the conversations of one workspace.
type Names interface {
	User(ctx context.Context, id string) (client.User, bool)
	Channel(ctx context.Context, id string) (client.Channel, bool)
}

// message is the part of a Slack message event that the mapping reads.
type message struct {
	Type       string     `json:"type"`
	Subtype    string     `json:"subtype"`
	Channel    channelRef `json:"channel"`
	User       string     `json:"user"`
	BotID      string     `json:"bot_id"`
	Username   string     `json:"username"`
	BotProfile *struct {
		Name string `json:"name"`
	} `json:"bot_profile"`
	Text        string `json:"text"`
	TS          string `json:"ts"`
	EventTS     string `json:"event_ts"`
	ThreadTS    string `json:"thread_ts"`
	Attachments []struct {
		Pretext    string `json:"pretext"`
		AuthorName string `json:"author_name"`
		Title      string `json:"title"`
		Text       string `json:"text"`
		Fallback   string `json:"fallback"`
	} `json:"attachments"`
	Files []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Mimetype   string `json:"mimetype"`
		Size       int64  `json:"size"`
		URLPrivate string `json:"url_private"`
	} `json:"files"`
	Edited *struct {
		User string `json:"user"`
		TS   string `json:"ts"`
	} `json:"edited"`
	Message   *message `json:"message"`
	Previous  *message `json:"previous_message"`
	DeletedTS string   `json:"deleted_ts"`
	Reaction  string   `json:"reaction"`
	ItemUser  string   `json:"item_user"`
	Item      struct {
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	} `json:"item"`
}

// channelRef is a channel field that is an id or an object {id, ...}.
type channelRef string

func (c *channelRef) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*c = channelRef(s)
		return nil
	}
	var o struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	*c = channelRef(o.ID)
	return nil
}

// Events maps one raw Slack event to protocol events. Most events give none.
func (t *Team) Events(ctx context.Context, raw json.RawMessage) []sdk.Event {
	var m message
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	ch := string(m.Channel)
	switch m.Type {
	case "message":
		return t.messageEvents(ctx, ch, m, raw)
	case "app_mention":
		ev := t.message(ctx, ch, m, raw)
		return []sdk.Event{asMention(ev)}
	case "reaction_added":
		return t.reaction(ctx, m, raw)
	case "member_joined_channel":
		return t.join(ctx, ch, m.User, m.EventTS, raw)
	case "channel_joined", "group_joined":
		return t.join(ctx, ch, t.SelfID, m.EventTS, raw)
	}
	return nil
}

// plain lists the message subtypes that are a new message.
var plain = map[string]bool{"": true, "bot_message": true, "thread_broadcast": true, "file_share": true, "me_message": true}

func (t *Team) messageEvents(ctx context.Context, ch string, m message, raw json.RawMessage) []sdk.Event {
	switch {
	case plain[m.Subtype]:
		ev := t.message(ctx, ch, m, raw)
		if t.mentions(m) {
			return []sdk.Event{ev, asMention(ev)}
		}
		return []sdk.Event{ev}
	case m.Subtype == "message_changed" && m.Message != nil:
		// An unfurl also changes a message; only a new text is an edit.
		if m.Previous != nil && m.Previous.Text == m.Message.Text {
			return nil
		}
		ev := t.message(ctx, ch, *m.Message, raw)
		ev.Type = TypeEdited
		at := m.TS
		if m.Message.Edited != nil && m.Message.Edited.TS != "" {
			at = m.Message.Edited.TS
		}
		ev.ID += ".edit." + at
		ev.TS = slackTime(at)
		extra := map[string]any{}
		if m.Previous != nil {
			extra["previous_text"] = m.Previous.Text
		}
		ev.Data = t.data(ch, m.Message.TS, raw, extra)
		return []sdk.Event{ev}
	case m.Subtype == "message_deleted" && m.DeletedTS != "":
		prev := message{TS: m.DeletedTS}
		if m.Previous != nil {
			prev = *m.Previous
		}
		ev := sdk.Event{
			ID: t.msgID(ch, m.DeletedTS) + ".delete", Type: TypeDeleted, TS: slackTime(m.TS),
			Room: t.room(ctx, ch), Thread: thread(prev), Sender: t.sender(ctx, prev),
			Data: t.data(ch, m.DeletedTS, raw, map[string]any{"previous_text": prev.Text}),
		}
		return []sdk.Event{ev}
	}
	return nil
}

// message maps one message in channel ch.
func (t *Team) message(ctx context.Context, ch string, m message, raw json.RawMessage) sdk.Event {
	ev := sdk.Event{
		ID: t.msgID(ch, m.TS), Type: TypeMessage, TS: slackTime(m.TS),
		Room: t.room(ctx, ch), Thread: thread(m), Sender: t.sender(ctx, m),
		Text: t.text(ctx, m), Data: t.data(ch, m.TS, raw, nil),
	}
	if ev.Thread != nil {
		ev.ReplyTo = t.msgID(ch, m.ThreadTS)
	}
	for _, f := range m.Files {
		ev.Attachments = append(ev.Attachments, sdk.Attachment{ID: f.ID, Name: f.Name, Mime: f.Mimetype, Size: f.Size, URL: f.URLPrivate})
	}
	return ev
}

func (t *Team) reaction(ctx context.Context, m message, raw json.RawMessage) []sdk.Event {
	ch := m.Item.Channel
	if ch == "" || m.Item.TS == "" {
		return nil
	}
	ev := sdk.Event{
		ID: t.msgID(ch, m.Item.TS) + "." + m.User + "." + m.Reaction, Type: TypeReact, TS: slackTime(m.EventTS),
		Room: t.room(ctx, ch), Sender: t.sender(ctx, message{User: m.User}), Text: ":" + m.Reaction + ":",
		Data: t.data(ch, m.Item.TS, raw, map[string]any{"emoji": m.Reaction, "message_id": t.msgID(ch, m.Item.TS), "item_user": m.ItemUser}),
	}
	return []sdk.Event{ev}
}

func (t *Team) join(ctx context.Context, ch, user, ts string, raw json.RawMessage) []sdk.Event {
	if ch == "" || user == "" {
		return nil
	}
	if ts == "" {
		ts = strconv.FormatFloat(float64(time.Now().UnixMicro())/1e6, 'f', 6, 64)
	}
	ev := sdk.Event{
		ID: t.ID + "/" + ch + ".join." + user + "." + ts, Type: TypeJoin, TS: slackTime(ts),
		Room: t.room(ctx, ch), Sender: t.sender(ctx, message{User: user}),
		Data: t.data(ch, "", raw, nil),
	}
	return []sdk.Event{ev}
}

// msgID is the event id of a message: "<team>/<channel>/<ts>".
func (t *Team) msgID(ch, ts string) string { return t.ID + "/" + ch + "/" + ts }

func asMention(ev sdk.Event) sdk.Event {
	ev.ID += ".mention"
	ev.Type = TypeMention
	return ev
}

// mentions reports whether another person names this account in m.
func (t *Team) mentions(m message) bool {
	if t.SelfID == "" || m.User == t.SelfID {
		return false
	}
	return strings.Contains(m.Text, "<@"+t.SelfID+">") || strings.Contains(m.Text, "<@"+t.SelfID+"|")
}

// room maps a channel id. The room id is "<team>/<channel>".
func (t *Team) room(ctx context.Context, ch string) *sdk.Room {
	r := &sdk.Room{ID: t.ID + "/" + ch, Kind: sdk.RoomChannel, Parent: t.Title}
	c, ok := t.Names.Channel(ctx, ch)
	switch {
	case strings.HasPrefix(ch, "D") || c.IM:
		r.Kind = sdk.RoomDM
		if c.User != "" {
			r.Name = t.userName(ctx, c.User)
		}
	case c.MPIM || strings.HasPrefix(c.Name, "mpdm-"):
		r.Kind = sdk.RoomGroup
		r.Name = groupName(c.Name)
	case ok && c.Name != "":
		r.Name = "#" + c.Name
	}
	return r
}

// groupName turns "mpdm-ana--bo--cy-1" into "ana, bo, cy".
func groupName(n string) string {
	n = strings.TrimPrefix(n, "mpdm-")
	if i := strings.LastIndex(n, "-"); i > 0 {
		if _, err := strconv.Atoi(n[i+1:]); err == nil {
			n = n[:i]
		}
	}
	return strings.Join(strings.Split(n, "--"), ", ")
}

func thread(m message) *sdk.Thread {
	if m.ThreadTS == "" || m.ThreadTS == m.TS {
		return nil
	}
	return &sdk.Thread{ID: m.ThreadTS}
}

func (t *Team) sender(ctx context.Context, m message) *sdk.Author {
	a := &sdk.Author{ID: m.User, Bot: m.BotID != "" || m.Subtype == "bot_message"}
	if a.ID == "" {
		a.ID = m.BotID
	}
	if a.ID == "" && m.Username != "" { // an incoming webhook has only a name
		a.ID, a.Bot = m.Username, true
	}
	if a.ID == "" {
		return nil
	}
	a.Self = t.SelfID != "" && m.User == t.SelfID
	switch {
	case m.User != "":
		u, ok := t.Names.User(ctx, m.User)
		a.Name = m.User
		if ok {
			a.Name = u.Label()
			a.Bot = a.Bot || u.Bot
		}
	case m.Username != "":
		a.Name = m.Username
	case m.BotProfile != nil:
		a.Name = m.BotProfile.Name
	}
	return a
}

func (t *Team) userName(ctx context.Context, id string) string {
	if u, ok := t.Names.User(ctx, id); ok {
		return u.Label()
	}
	return id
}

var (
	userRE    = regexp.MustCompile(`<@([UW][A-Z0-9]+)(?:\|[^>]*)?>`)
	channelRE = regexp.MustCompile(`<#([CGD][A-Z0-9]+)(?:\|([^>]*))?>`)
	specialRE = regexp.MustCompile(`<!(here|channel|everyone)(?:\|[^>]*)?>`)
	linkRE    = regexp.MustCompile(`<((?:https?|mailto):[^|>]+)(?:\|([^>]+))?>`)
)

// text is what a person sees: the body with names for the mentions, then
// the readable parts of the attachments, one part per line.
func (t *Team) text(ctx context.Context, m message) string {
	parts := []string{t.readable(ctx, m.Text)}
	for _, a := range m.Attachments {
		body := a.Text
		if body == "" && a.Title == "" {
			body = a.Fallback
		}
		for _, p := range []string{a.Pretext, a.AuthorName, a.Title, body} {
			parts = append(parts, t.readable(ctx, p))
		}
	}
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}

// readable replaces the Slack markup of mentions and links.
func (t *Team) readable(ctx context.Context, s string) string {
	s = userRE.ReplaceAllStringFunc(s, func(x string) string {
		return "@" + t.userName(ctx, userRE.FindStringSubmatch(x)[1])
	})
	s = channelRE.ReplaceAllStringFunc(s, func(x string) string {
		g := channelRE.FindStringSubmatch(x)
		if g[2] != "" {
			return "#" + g[2]
		}
		if c, ok := t.Names.Channel(ctx, g[1]); ok && c.Name != "" {
			return "#" + c.Name
		}
		return "#" + g[1]
	})
	s = specialRE.ReplaceAllString(s, "@$1")
	s = linkRE.ReplaceAllStringFunc(s, func(x string) string {
		g := linkRE.FindStringSubmatch(x)
		if g[2] == "" || g[2] == g[1] {
			return g[1]
		}
		return g[2] + " (" + g[1] + ")"
	})
	return html.UnescapeString(s)
}

// data is the raw event with the workspace labels.
func (t *Team) data(ch, ts string, raw json.RawMessage, extra map[string]any) json.RawMessage {
	d := map[string]any{"workspace": t.Workspace, "team_id": t.ID, "team_name": t.Title, "channel_id": ch, "event": raw}
	if ts != "" {
		d["ts"] = ts
	}
	for k, v := range extra {
		d[k] = v
	}
	b, _ := json.Marshal(d) //nolint:errcheck // strings and raw JSON always encode
	return b
}

// slackTime turns a Slack ts ("seconds.micros") into the event time.
func slackTime(ts string) string {
	s, frac, _ := strings.Cut(ts, ".")
	sec, err := strconv.ParseInt(s, 10, 64)
	if err != nil || sec <= 0 {
		return sdk.FormatTime(time.Now())
	}
	frac = (frac + "000000000")[:9]
	ns, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		ns = 0
	}
	return sdk.FormatTime(time.Unix(sec, ns))
}
