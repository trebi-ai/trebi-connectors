package serve

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/trebi-ai/trebi-connectors/connectors/discord-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/discord-cli/internal/gateway"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Intents: guilds, guild messages, guild reactions, DMs, DM reactions, and
// message content. Message content is a privileged intent.
const Intents = 1<<0 | 1<<9 | 1<<10 | 1<<12 | 1<<13 | 1<<15

// Gateway close codes that a reconnect does not fix.
const (
	closeAuthFailed        = 4004
	closeDisallowedIntents = 4014
)

// Run holds the gateway connection until ctx ends. It reconnects with a
// backoff of up to one minute.
func (a *Adapter) Run(ctx context.Context, e sdk.Emitter) error {
	backoff := time.Second
	first := true
	for ctx.Err() == nil {
		err := a.listen(ctx, e, first)
		if ctx.Err() != nil {
			return nil
		}
		var ce *websocket.CloseError
		switch {
		case errors.As(err, &ce) && ce.Code == closeAuthFailed:
			a.mu.Lock()
			a.revoked = true
			a.mu.Unlock()
			if serr := e.Status(sdk.Status{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked}); serr != nil {
				return serr
			}
			<-ctx.Done()
			return nil
		case errors.As(err, &ce) && ce.Code == closeDisallowedIntents:
			if serr := e.Status(sdk.Status{State: sdk.StateError, Message: "turn on the Message Content intent of the bot in the Discord developer portal"}); serr != nil {
				return serr
			}
			<-ctx.Done()
			return nil
		case errors.Is(err, errConnected):
			backoff = time.Second
		}
		first = false
		slog.Warn("discord gateway", "err", err, "retry_in", backoff)
		if serr := e.Status(sdk.Status{State: sdk.StateConnecting, Message: err.Error()}); serr != nil {
			return serr
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
		backoff = min(backoff*2, time.Minute)
	}
	return nil
}

// errConnected wraps a disconnect after a good connect, so Run resets the
// backoff.
var errConnected = errors.New("connection lost")

// listen runs one gateway connection.
func (a *Adapter) listen(ctx context.Context, e sdk.Emitter, first bool) error {
	gw := gateway.New(a.rest.Token, Intents)
	gw.DialURL = a.dialURL
	done := make(chan struct{})
	defer close(done)
	go func() { // ends with ctx or with this connection
		select {
		case <-ctx.Done():
			gw.Close()
		case <-done:
		}
	}()
	defer gw.Close()
	ready, err := gw.Connect()
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.self, a.revoked = &ready.User, false
	a.mu.Unlock()
	if !first {
		if err := e.Status(sdk.Status{State: sdk.StateConnected, Account: account(ready.User)}); err != nil {
			return err
		}
	}
	var emitErr error
	err = gw.Listen(func(t string, data json.RawMessage) {
		if emitErr != nil {
			return
		}
		if ev, ok := a.dispatch(ctx, t, data); ok {
			emitErr = e.Event(ev)
			if emitErr != nil {
				gw.Close()
			}
		}
	})
	if emitErr != nil {
		return emitErr
	}
	if err == nil {
		err = errors.New("gateway closed")
	}
	return errors.Join(errConnected, err)
}

// dispatch keeps the caches warm and maps a gateway event to a protocol
// event.
func (a *Adapter) dispatch(ctx context.Context, t string, data json.RawMessage) (sdk.Event, bool) {
	switch t {
	case "GUILD_CREATE":
		var g struct {
			ID       string           `json:"id"`
			Name     string           `json:"name"`
			Channels []client.Channel `json:"channels"`
			Threads  []client.Channel `json:"threads"`
		}
		if json.Unmarshal(data, &g) != nil {
			return sdk.Event{}, false
		}
		a.mu.Lock()
		a.guilds[g.ID] = g.Name
		a.mu.Unlock()
		for _, ch := range append(g.Channels, g.Threads...) {
			ch.GuildID = g.ID
			a.cache(ch)
		}
	case "CHANNEL_CREATE", "CHANNEL_UPDATE", "THREAD_CREATE", "THREAD_UPDATE":
		var ch client.Channel
		if json.Unmarshal(data, &ch) == nil {
			a.cache(ch)
		}
	case "CHANNEL_DELETE", "THREAD_DELETE":
		var ch client.Channel
		if json.Unmarshal(data, &ch) == nil {
			a.mu.Lock()
			delete(a.channels, ch.ID)
			a.mu.Unlock()
		}
	case "THREAD_LIST_SYNC":
		var s struct {
			Threads []client.Channel `json:"threads"`
		}
		if json.Unmarshal(data, &s) == nil {
			for _, th := range s.Threads {
				a.cache(th)
			}
		}
	case "MESSAGE_CREATE":
		var m client.Message
		if json.Unmarshal(data, &m) != nil || m.ID == "" {
			return sdk.Event{}, false
		}
		return a.messageEvent(ctx, m, data), true
	case "MESSAGE_REACTION_ADD":
		return a.reactionEvent(ctx, data)
	}
	return sdk.Event{}, false
}

// messageEvent maps a message. A message in a thread has the parent
// channel as its room. raw is the gateway payload, when there is one.
func (a *Adapter) messageEvent(ctx context.Context, m client.Message, raw json.RawMessage) sdk.Event {
	a.remember(m.ID, m.ChannelID)
	room, thread := a.place(ctx, m.ChannelID)
	if room.Kind == sdk.RoomDM && room.Name == "" && m.Author != nil && !a.isSelf(m.Author.ID) {
		room.Name = displayName(*m.Author)
	}
	if room.Kind == sdk.RoomDM {
		if err := a.keepDM(room); err != nil {
			slog.Warn("keep dm room", "err", err)
		}
	}
	ev := sdk.Event{
		ID: m.ID, Type: "message", TS: sdk.FormatTime(m.Timestamp),
		Room: &room, Thread: thread, Text: messageText(m), Raw: raw,
	}
	data := map[string]any{}
	if m.GuildID != "" {
		data["guild_id"] = m.GuildID
	}
	if m.Author != nil {
		ev.Sender = &sdk.Author{ID: m.Author.ID, Name: displayName(*m.Author), Self: a.isSelf(m.Author.ID), Bot: m.Author.Bot}
	}
	if len(m.Embeds) > 0 {
		data["embeds"] = m.Embeds
	}
	if m.EditedTimestamp != nil {
		data["edited_at"] = sdk.FormatTime(*m.EditedTimestamp)
	}
	if len(data) > 0 {
		ev.Data, _ = json.Marshal(data) //nolint:errcheck // strings and plain structs always encode
	}
	const typeReply = 19
	if m.Type == typeReply && m.MessageReference != nil {
		ev.ReplyTo = m.MessageReference.MessageID
	}
	for _, at := range m.Attachments {
		ev.Attachments = append(ev.Attachments, sdk.Attachment{
			ID: at.ID, Name: at.Filename, Mime: at.ContentType, Size: int64(at.Size), URL: at.URL,
		})
	}
	return ev
}

// messageText is the content, then the readable parts of each embed.
func messageText(m client.Message) string {
	parts := []string{m.Content}
	for _, e := range m.Embeds {
		if e.Author != nil {
			parts = append(parts, e.Author.Name)
		}
		parts = append(parts, e.Title, e.Description)
		for _, f := range e.Fields {
			name, value := strings.TrimSpace(f.Name), strings.TrimSpace(f.Value)
			if name != "" && value != "" {
				parts = append(parts, name+": "+value)
			} else {
				parts = append(parts, name+value)
			}
		}
		if e.Footer != nil {
			parts = append(parts, e.Footer.Text)
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

// reactionEvent maps MESSAGE_REACTION_ADD. The id is
// "<message>.<user>.<emoji>", so the same reaction has the same id.
func (a *Adapter) reactionEvent(ctx context.Context, data json.RawMessage) (sdk.Event, bool) {
	var r struct {
		UserID    string                      `json:"user_id"`
		ChannelID string                      `json:"channel_id"`
		MessageID string                      `json:"message_id"`
		GuildID   string                      `json:"guild_id"`
		Emoji     client.Emoji                `json:"emoji"`
		Member    *struct{ User client.User } `json:"member"`
	}
	if json.Unmarshal(data, &r) != nil || r.MessageID == "" {
		return sdk.Event{}, false
	}
	emoji := r.Emoji.Name
	if r.Emoji.ID != "" {
		emoji += ":" + r.Emoji.ID
	}
	a.remember(r.MessageID, r.ChannelID)
	room, thread := a.place(ctx, r.ChannelID)
	sender := &sdk.Author{ID: r.UserID, Self: a.isSelf(r.UserID)}
	if r.Member != nil {
		sender.Name = displayName(r.Member.User)
	}
	body, _ := json.Marshal(map[string]string{"emoji": emoji, "message_id": r.MessageID}) //nolint:errcheck // strings always encode
	return sdk.Event{
		ID: r.MessageID + "." + r.UserID + "." + emoji, Type: "reaction", TS: sdk.FormatTime(time.Now()),
		Room: &room, Thread: thread, Sender: sender, Data: body, Raw: data,
	}, true
}

// place returns the room and the thread of a channel id.
func (a *Adapter) place(ctx context.Context, channelID string) (sdk.Room, *sdk.Thread) {
	ch, err := a.channel(ctx, channelID)
	if err != nil {
		slog.Warn("read channel", "channel", channelID, "err", err)
		return sdk.Room{ID: channelID}, nil
	}
	if !isThread(ch.Type) {
		room := a.roomOf(ch)
		room.LastActivityAt = ""
		return room, nil
	}
	th := threadOf(ch)
	th.LastActivityAt = ""
	parent, err := a.channel(ctx, ch.ParentID)
	if err != nil {
		return sdk.Room{ID: ch.ParentID}, &th
	}
	room := a.roomOf(parent)
	room.LastActivityAt = ""
	return room, &th
}

func (a *Adapter) isSelf(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.self != nil && a.self.ID == id
}
