// Package serve is the trebi-connector/1 adapter of discord-cli. The
// gateway gives the events; the REST API serves the requests.
package serve

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/src/discord-cli/internal/client"
)

// Discord channel types.
const (
	typeText         = 0
	typeDM           = 1
	typeGroupDM      = 3
	typeAnnouncement = 5
	typeNewsThread   = 10
	typePublicThread = 11
	typePrivThread   = 12
)

// Name is the adapter name in initialize.
const Name = "discord-cli"

// SeenEmoji is the reaction that marks a message as seen.
const SeenEmoji = "👀"

// dmFile keeps the DM rooms: a bot cannot list its DMs through REST.
const dmFile = "discord-dms.json"

// maxAttachment bounds one outbound file (the Discord limit without boost).
const maxAttachment = 25 << 20

// Events, Features, and Limits are what the adapter and its sandbox declare.
var (
	Events   = []sdk.EventDecl{{Type: "message"}, {Type: "reaction"}}
	Features = []string{
		sdk.FeatureRoomsList, sdk.FeatureRoomsOpen, sdk.FeatureThreads, sdk.FeatureThreadsCreate,
		sdk.FeatureHistory, sdk.FeatureReplay, sdk.FeatureTyping, sdk.FeatureSeen,
		sdk.FeatureReactions, sdk.FeatureEdit, sdk.FeatureAttachmentsIn, sdk.FeatureAttachmentsOut,
	}
	Limits = sdk.Limits{MaxText: 2000, Formats: []string{sdk.FormatMarkdown}}
)

// Adapter serves one bot token.
type Adapter struct {
	rest     *client.Client
	version  string
	stateDir string
	dialURL  string // gateway URL override for tests
	fetch    *http.Client

	mu       sync.Mutex
	self     *client.User
	revoked  bool
	guilds   map[string]string // id → name
	channels map[string]client.Channel
	dms      []sdk.Room
	msgChan  map[string]string // message id → channel id, for edit, seen, and reactions in threads
	msgOrder []string
}

var (
	_ sdk.Runner         = (*Adapter)(nil)
	_ sdk.StatusReporter = (*Adapter)(nil)
	_ sdk.Sender         = (*Adapter)(nil)
	_ sdk.RoomLister     = (*Adapter)(nil)
	_ sdk.RoomGetter     = (*Adapter)(nil)
	_ sdk.RoomOpener     = (*Adapter)(nil)
	_ sdk.ThreadLister   = (*Adapter)(nil)
	_ sdk.ThreadCreator  = (*Adapter)(nil)
	_ sdk.Historian      = (*Adapter)(nil)
	_ sdk.Replayer       = (*Adapter)(nil)
	_ sdk.Typer          = (*Adapter)(nil)
	_ sdk.Seer           = (*Adapter)(nil)
	_ sdk.Reactor        = (*Adapter)(nil)
	_ sdk.Editor         = (*Adapter)(nil)
)

// New builds the adapter. stateDir keeps the DM rooms; empty keeps them in
// memory. The client gets NoRetry, so a 429 goes to the daemon.
func New(rest *client.Client, version, stateDir string) (*Adapter, error) {
	rest.NoRetry = true
	a := &Adapter{
		rest: rest, version: version, stateDir: stateDir,
		fetch:    &http.Client{Timeout: 20 * time.Second},
		guilds:   map[string]string{},
		channels: map[string]client.Channel{},
		msgChan:  map[string]string{},
	}
	if stateDir == "" {
		return a, nil
	}
	data, err := os.ReadFile(filepath.Join(stateDir, dmFile))
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read dm rooms: %w", err)
	}
	if err := json.Unmarshal(data, &a.dms); err != nil {
		return nil, fmt.Errorf("parse dm rooms: %w", err)
	}
	return a, nil
}

// Initialize checks the token. A rejected token is not an error: the
// status reports auth_required with reason revoked.
func (a *Adapter) Initialize(ctx context.Context, _ sdk.InitializeParams) (sdk.InitializeResult, error) {
	res := sdk.InitializeResult{
		Adapter:  sdk.AdapterInfo{Name: Name, Version: a.version},
		Events:   Events,
		Features: Features,
		Limits:   Limits,
	}
	var me client.User
	err := a.rest.DoJSON(ctx, "GET", "/users/@me", nil, &me)
	var apiErr *client.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized:
		a.mu.Lock()
		a.revoked = true
		a.mu.Unlock()
		return res, nil
	case err != nil:
		return res, a.wrap(err)
	}
	a.mu.Lock()
	a.self = &me
	a.mu.Unlock()
	res.Account = account(me)
	return res, nil
}

// AuthStatus is connected, or auth_required when Discord rejects the token.
func (a *Adapter) AuthStatus(context.Context) (sdk.AuthState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.revoked || a.self == nil {
		return sdk.AuthState{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked}, nil
	}
	return sdk.AuthState{State: sdk.StateConnected, Account: account(*a.self)}, nil
}

// Send posts a message to the thread, or else to the room.
func (a *Adapter) Send(ctx context.Context, m sdk.SendParams) (sdk.SendResult, error) {
	target := cmp.Or(m.Thread, m.Room)
	body := map[string]any{"content": m.Text}
	if m.ReplyTo != "" {
		body["message_reference"] = map[string]any{"message_id": m.ReplyTo, "fail_if_not_exists": false}
	}
	var msg client.Message
	if len(m.Attachments) == 0 {
		if err := a.rest.DoJSON(ctx, "POST", "/channels/"+target+"/messages", body, &msg); err != nil {
			return sdk.SendResult{}, a.wrap(err)
		}
	} else {
		files := make([]client.FileUpload, 0, len(m.Attachments))
		for _, at := range m.Attachments {
			f, err := a.load(ctx, at)
			if err != nil {
				return sdk.SendResult{}, err
			}
			files = append(files, f)
		}
		payload, err := json.Marshal(body)
		if err != nil {
			return sdk.SendResult{}, err
		}
		sent, err := a.rest.Upload(ctx, "/channels/"+target+"/messages", map[string]string{"payload_json": string(payload)}, files)
		if err != nil {
			return sdk.SendResult{}, a.wrap(err)
		}
		msg = *sent
	}
	a.remember(msg.ID, target)
	return sdk.SendResult{MessageID: msg.ID, Thread: m.Thread}, nil
}

// load reads an outbound attachment from its path or its URL.
func (a *Adapter) load(ctx context.Context, at sdk.Attachment) (client.FileUpload, error) {
	name := at.Name
	switch {
	case at.Path != "":
		data, err := os.ReadFile(at.Path)
		if err != nil {
			return client.FileUpload{}, sdk.Invalid("attachment: " + err.Error())
		}
		return client.FileUpload{Filename: cmp.Or(name, filepath.Base(at.Path)), Data: data}, nil
	case at.URL != "":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, at.URL, nil)
		if err != nil {
			return client.FileUpload{}, sdk.Invalid("attachment: " + err.Error())
		}
		resp, err := a.fetch.Do(req)
		if err != nil {
			return client.FileUpload{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return client.FileUpload{}, sdk.Permanent(fmt.Sprintf("attachment: GET %s: %s", at.URL, resp.Status))
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachment+1))
		if err != nil {
			return client.FileUpload{}, err
		}
		if len(data) > maxAttachment {
			return client.FileUpload{}, sdk.Invalid("attachment is larger than 25 MiB")
		}
		if name == "" {
			u, _ := url.Parse(at.URL) //nolint:errcheck // the request parsed it already
			name = cmp.Or(filepath.Base(u.Path), "file")
		}
		return client.FileUpload{Filename: name, Data: data}, nil
	}
	return client.FileUpload{}, sdk.Invalid("attachment needs a path or a url")
}

// Edit changes the text of a message the bot sent.
func (a *Adapter) Edit(ctx context.Context, p sdk.EditParams) error {
	path := "/channels/" + a.channelOf(p.MessageID, p.Room) + "/messages/" + p.MessageID
	return a.wrap(a.rest.DoJSON(ctx, "PATCH", path, map[string]any{"content": p.Text}, nil))
}

// History reads the messages before q.Before, oldest first.
func (a *Adapter) History(ctx context.Context, q sdk.HistoryQuery) (sdk.EventPage, error) {
	limit := min(max(q.Limit, 1), 100)
	if q.Limit == 0 {
		limit = 50
	}
	v := url.Values{"limit": {strconv.Itoa(limit)}}
	if q.Before != "" {
		v.Set("before", q.Before)
	}
	var msgs []client.Message
	if err := a.rest.DoJSON(ctx, "GET", "/channels/"+cmp.Or(q.Thread, q.Room)+"/messages?"+v.Encode(), nil, &msgs); err != nil {
		return sdk.EventPage{}, a.wrap(err)
	}
	slices.Reverse(msgs)
	page := sdk.EventPage{Events: make([]sdk.Event, 0, len(msgs))}
	for _, m := range msgs {
		page.Events = append(page.Events, a.messageEvent(ctx, m, nil))
	}
	if len(msgs) == limit {
		page.Next = msgs[0].ID
	}
	return page, nil
}

// Replay reads the messages after the cursor from every channel, active
// thread, and known DM with newer activity. Reactions do not replay.
func (a *Adapter) Replay(ctx context.Context, after string, limit int) ([]sdk.Event, bool, error) {
	cursor, ok := snowflake(after)
	if !ok {
		return nil, false, nil
	}
	if limit <= 0 {
		limit = 500
	}
	chans, err := a.activeSince(ctx, cursor)
	if err != nil {
		return nil, false, err
	}
	var msgs []client.Message
	for _, ch := range chans {
		from := strconv.FormatUint(cursor, 10)
		for len(msgs) < limit*2 {
			var page []client.Message
			if err := a.rest.DoJSON(ctx, "GET", "/channels/"+ch+"/messages?limit=100&after="+from, nil, &page); err != nil {
				return nil, false, a.wrap(err)
			}
			msgs = append(msgs, page...)
			if len(page) < 100 {
				break
			}
			from = slices.MaxFunc(page, bySnowflake).ID
		}
	}
	slices.SortFunc(msgs, bySnowflake)
	if len(msgs) > limit {
		msgs = msgs[:limit]
	}
	out := make([]sdk.Event, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, a.messageEvent(ctx, m, nil))
	}
	return out, true, nil
}

// activeSince lists the channel ids with a last message after cursor.
func (a *Adapter) activeSince(ctx context.Context, cursor uint64) ([]string, error) {
	var out []string
	newer := func(ch client.Channel) bool {
		id, ok := snowflake(ch.LastMessageID)
		return ok && id > cursor
	}
	guilds, err := a.guildList(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range guilds {
		chans, err := a.guildChannels(ctx, g.ID)
		if err != nil {
			return nil, err
		}
		for _, ch := range chans {
			if (ch.Type == typeText || ch.Type == typeAnnouncement) && newer(ch) {
				out = append(out, ch.ID)
			}
		}
		var active client.ActiveThreadsResponse
		if err := a.rest.DoJSON(ctx, "GET", "/guilds/"+g.ID+"/threads/active", nil, &active); err != nil {
			return nil, a.wrap(err)
		}
		for _, th := range active.Threads {
			a.cache(th)
			if newer(th) {
				out = append(out, th.ID)
			}
		}
	}
	a.mu.Lock()
	dms := slices.Clone(a.dms)
	a.mu.Unlock()
	for _, dm := range dms {
		ch, err := a.channel(ctx, dm.ID)
		if err != nil {
			continue
		}
		if newer(ch) {
			out = append(out, ch.ID)
		}
	}
	return out, nil
}

// ListRooms lists the text channels of every guild and the known DMs. The
// cursor is an offset.
func (a *Adapter) ListRooms(ctx context.Context, q sdk.RoomQuery) (sdk.RoomPage, error) {
	var rooms []sdk.Room
	if q.Kind == "" || q.Kind == sdk.RoomChannel {
		guilds, err := a.guildList(ctx)
		if err != nil {
			return sdk.RoomPage{}, err
		}
		for _, g := range guilds {
			chans, err := a.guildChannels(ctx, g.ID)
			if err != nil {
				return sdk.RoomPage{}, err
			}
			for _, ch := range chans {
				if ch.Type == typeText || ch.Type == typeAnnouncement {
					rooms = append(rooms, a.roomOf(ch))
				}
			}
		}
	}
	if q.Kind == "" || q.Kind == sdk.RoomDM {
		a.mu.Lock()
		rooms = append(rooms, a.dms...)
		a.mu.Unlock()
	}
	query := strings.ToLower(q.Query)
	rooms = slices.DeleteFunc(rooms, func(r sdk.Room) bool {
		return query != "" && !strings.Contains(strings.ToLower(r.Name), query)
	})
	off, _ := strconv.Atoi(q.Cursor) //nolint:errcheck // a bad cursor starts at the top
	off = min(max(off, 0), len(rooms))
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	end := min(off+limit, len(rooms))
	page := sdk.RoomPage{Rooms: rooms[off:end]}
	if end < len(rooms) {
		page.Next = strconv.Itoa(end)
	}
	return page, nil
}

// GetRoom reads one channel.
func (a *Adapter) GetRoom(ctx context.Context, id string) (sdk.Room, error) {
	ch, err := a.channel(ctx, id)
	if err != nil {
		return sdk.Room{}, err
	}
	return a.roomOf(ch), nil
}

// OpenRoom opens the DM channel with a user id.
func (a *Adapter) OpenRoom(ctx context.Context, user string) (sdk.Room, error) {
	var ch client.Channel
	if err := a.rest.DoJSON(ctx, "POST", "/users/@me/channels", map[string]any{"recipient_id": user}, &ch); err != nil {
		return sdk.Room{}, a.wrap(err)
	}
	a.cache(ch)
	room := a.roomOf(ch)
	return room, a.keepDM(room)
}

// ListThreads lists the active threads of a channel, then its archived
// public threads. The cursor "a:<time>" pages the archived threads.
func (a *Adapter) ListThreads(ctx context.Context, q sdk.ThreadQuery) (sdk.ThreadPage, error) {
	ch, err := a.channel(ctx, q.Room)
	if err != nil {
		return sdk.ThreadPage{}, err
	}
	page := sdk.ThreadPage{Threads: []sdk.Thread{}}
	if ch.GuildID == "" {
		return page, nil
	}
	limit := min(max(q.Limit, 1), 100)
	before, paging := strings.CutPrefix(q.Cursor, "a:")
	if !paging {
		var active client.ActiveThreadsResponse
		if err := a.rest.DoJSON(ctx, "GET", "/guilds/"+ch.GuildID+"/threads/active", nil, &active); err != nil {
			return page, a.wrap(err)
		}
		for _, th := range active.Threads {
			a.cache(th)
			if th.ParentID == q.Room {
				page.Threads = append(page.Threads, threadOf(th))
			}
		}
	}
	v := url.Values{"limit": {strconv.Itoa(limit)}}
	if before != "" {
		v.Set("before", before)
	}
	var archived client.ArchivedThreadsResponse
	if err := a.rest.DoJSON(ctx, "GET", "/channels/"+q.Room+"/threads/archived/public?"+v.Encode(), nil, &archived); err != nil {
		return page, a.wrap(err)
	}
	for _, th := range archived.Threads {
		page.Threads = append(page.Threads, threadOf(th))
	}
	if archived.HasMore && len(archived.Threads) > 0 {
		if md := archived.Threads[len(archived.Threads)-1].ThreadMetadata; md != nil {
			page.Next = "a:" + md.ArchiveTimestamp.UTC().Format(time.RFC3339Nano)
		}
	}
	return page, nil
}

// CreateThread starts a public thread, on a message when one is given.
func (a *Adapter) CreateThread(ctx context.Context, p sdk.CreateThreadParams) (sdk.Thread, error) {
	body := map[string]any{"name": cmp.Or(p.Title, "Thread")}
	path := "/channels/" + p.Room + "/threads"
	if p.FromMessage != "" {
		path = "/channels/" + p.Room + "/messages/" + p.FromMessage + "/threads"
	} else {
		body["type"] = typePublicThread
	}
	var th client.Channel
	if err := a.rest.DoJSON(ctx, "POST", path, body, &th); err != nil {
		return sdk.Thread{}, a.wrap(err)
	}
	a.cache(th)
	return threadOf(th), nil
}

// Typing shows the typing indicator for about ten seconds.
func (a *Adapter) Typing(ctx context.Context, room, thread string) error {
	return a.wrap(a.rest.DoNoBody(ctx, "POST", "/channels/"+cmp.Or(thread, room)+"/typing"))
}

// Seen adds the SeenEmoji reaction: a bot has no read receipts.
func (a *Adapter) Seen(ctx context.Context, room, messageID string) error {
	return a.React(ctx, room, messageID, SeenEmoji)
}

// React adds a reaction. A custom emoji is "name:id".
func (a *Adapter) React(ctx context.Context, room, messageID, emoji string) error {
	e := emoji
	if !strings.Contains(e, ":") {
		e = url.PathEscape(e)
	}
	path := "/channels/" + a.channelOf(messageID, room) + "/messages/" + messageID + "/reactions/" + e + "/@me"
	return a.wrap(a.rest.DoNoBody(ctx, "PUT", path))
}

func (a *Adapter) guildList(ctx context.Context) ([]client.Guild, error) {
	var guilds []client.Guild
	if err := a.rest.DoJSON(ctx, "GET", "/users/@me/guilds", nil, &guilds); err != nil {
		return nil, a.wrap(err)
	}
	a.mu.Lock()
	for _, g := range guilds {
		a.guilds[g.ID] = g.Name
	}
	a.mu.Unlock()
	return guilds, nil
}

func (a *Adapter) guildChannels(ctx context.Context, guild string) ([]client.Channel, error) {
	var chans []client.Channel
	if err := a.rest.DoJSON(ctx, "GET", "/guilds/"+guild+"/channels", nil, &chans); err != nil {
		return nil, a.wrap(err)
	}
	for _, ch := range chans {
		a.cache(ch)
	}
	return chans, nil
}

// channel reads a channel from the cache, or else from REST.
func (a *Adapter) channel(ctx context.Context, id string) (client.Channel, error) {
	a.mu.Lock()
	ch, ok := a.channels[id]
	a.mu.Unlock()
	if ok {
		return ch, nil
	}
	if err := a.rest.DoJSON(ctx, "GET", "/channels/"+id, nil, &ch); err != nil {
		return ch, a.wrap(err)
	}
	a.cache(ch)
	return ch, nil
}

func (a *Adapter) cache(ch client.Channel) {
	if ch.ID == "" {
		return
	}
	a.mu.Lock()
	a.channels[ch.ID] = ch
	a.mu.Unlock()
}

// roomOf maps a channel that is not a thread to a room.
func (a *Adapter) roomOf(ch client.Channel) sdk.Room {
	r := sdk.Room{ID: ch.ID, Name: ch.Name, Kind: sdk.RoomChannel, LastActivityAt: activity(ch.LastMessageID)}
	switch ch.Type {
	case typeDM:
		r.Kind = sdk.RoomDM
		if len(ch.Recipients) > 0 {
			r.Name = displayName(ch.Recipients[0])
		}
	case typeGroupDM:
		r.Kind = sdk.RoomGroup
		if r.Name == "" {
			names := make([]string, 0, len(ch.Recipients))
			for _, u := range ch.Recipients {
				names = append(names, displayName(u))
			}
			r.Name = strings.Join(names, ", ")
		}
	default:
		a.mu.Lock()
		r.Parent = a.guilds[ch.GuildID]
		a.mu.Unlock()
	}
	return r
}

// keepDM stores a DM room, so rooms/list and replay know it.
func (a *Adapter) keepDM(r sdk.Room) error {
	if r.Kind != sdk.RoomDM {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	i := slices.IndexFunc(a.dms, func(d sdk.Room) bool { return d.ID == r.ID })
	if i >= 0 && a.dms[i].Name == r.Name {
		return nil
	}
	r.LastActivityAt = ""
	if i >= 0 {
		a.dms[i] = r
	} else {
		a.dms = append(a.dms, r)
	}
	if a.stateDir == "" {
		return nil
	}
	data, err := json.Marshal(a.dms)
	if err != nil {
		return err
	}
	tmp := filepath.Join(a.stateDir, dmFile+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write dm rooms: %w", err)
	}
	return os.Rename(tmp, filepath.Join(a.stateDir, dmFile))
}

// maxRemembered bounds the message → channel map.
const maxRemembered = 10000

func (a *Adapter) remember(msgID, channelID string) {
	if msgID == "" || channelID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.msgChan[msgID]; !ok {
		a.msgOrder = append(a.msgOrder, msgID)
	}
	a.msgChan[msgID] = channelID
	if len(a.msgOrder) > maxRemembered {
		delete(a.msgChan, a.msgOrder[0])
		a.msgOrder = a.msgOrder[1:]
	}
}

// channelOf is the channel of a known message (a thread), or else room.
func (a *Adapter) channelOf(msgID, room string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cmp.Or(a.msgChan[msgID], room)
}

// wrap maps a Discord API error to a protocol error.
func (a *Adapter) wrap(err error) error {
	var e *client.APIError
	if !errors.As(err, &e) {
		return err
	}
	switch {
	case e.Status == http.StatusUnauthorized:
		a.mu.Lock()
		a.revoked = true
		a.mu.Unlock()
		return sdk.AuthRequired("Discord rejects the bot token")
	case e.Status == http.StatusTooManyRequests:
		return sdk.RateLimited("Discord rate limit", e.RetryAfter)
	case e.Status == http.StatusNotFound:
		return sdk.NotFound(e.Error())
	case e.Status == http.StatusBadRequest:
		return sdk.Invalid(e.Error())
	case e.Status >= 500:
		return sdk.Transient(e.Error())
	}
	return sdk.Permanent(e.Error())
}

func threadOf(ch client.Channel) sdk.Thread {
	return sdk.Thread{ID: ch.ID, Title: ch.Name, LastActivityAt: activity(ch.LastMessageID)}
}

func account(u client.User) *sdk.Account {
	return &sdk.Account{ID: u.ID, Name: displayName(u)}
}

func displayName(u client.User) string {
	return cmp.Or(u.GlobalName, u.Username)
}

// snowflake parses a Discord id. An event id "<message>.<user>.<emoji>"
// parses as its message id.
func snowflake(id string) (uint64, bool) {
	id, _, _ = strings.Cut(id, ".")
	n, err := strconv.ParseUint(id, 10, 64)
	return n, err == nil && n > 0
}

func bySnowflake(x, y client.Message) int {
	a, _ := snowflake(x.ID)
	b, _ := snowflake(y.ID)
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// activity is the time of a snowflake id, in the event time format.
func activity(id string) string {
	n, ok := snowflake(id)
	if !ok {
		return ""
	}
	return sdk.FormatTime(time.UnixMilli(int64(n>>22) + 1420070400000))
}

func isThread(t int) bool {
	return t == typeNewsThread || t == typePublicThread || t == typePrivThread
}
