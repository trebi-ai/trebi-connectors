package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/store"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/wa"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// AdapterName is the adapter name in initialize.
const AdapterName = "whatsapp-cli"

// maxDownload bounds one inbound or outbound attachment.
const maxDownload = 100 << 20

// Protocol declarations. The sandbox uses the same values.
var (
	AdapterEvents   = []sdk.EventDecl{{Type: "message"}, {Type: "reaction"}}
	AdapterFeatures = []string{
		sdk.FeatureRoomsList, sdk.FeatureRoomsOpen, sdk.FeatureHistory, sdk.FeatureReplay,
		sdk.FeatureTyping, sdk.FeatureSeen, sdk.FeatureReactions,
		sdk.FeatureAttachmentsIn, sdk.FeatureAttachmentsOut,
	}
	AdapterLimits = sdk.Limits{MaxText: 65536, Formats: []string{sdk.FormatText}}
	AdapterLogin  = []string{sdk.StepQR}
)

// Adapter serves the trebi-connector/1 protocol over one WhatsApp session.
type Adapter struct {
	app   *App
	fetch *http.Client

	mu    sync.Mutex
	queue []any
	wake  chan struct{}
}

// NewAdapter returns the protocol adapter of a. a must have an open
// WhatsApp client.
func NewAdapter(a *App) *Adapter {
	return &Adapter{app: a, fetch: &http.Client{Timeout: 2 * time.Minute}, wake: make(chan struct{}, 1)}
}

func (d *Adapter) Initialize(ctx context.Context, in sdk.InitializeParams) (sdk.InitializeResult, error) {
	return sdk.InitializeResult{
		Adapter:  sdk.AdapterInfo{Name: AdapterName, Version: d.app.Version()},
		Account:  d.account(),
		Events:   AdapterEvents,
		Features: AdapterFeatures,
		Limits:   AdapterLimits,
		Login:    AdapterLogin,
	}, nil
}

func (d *Adapter) account() *sdk.Account {
	jid, name := d.app.wa.Account()
	if jid.IsEmpty() {
		return nil
	}
	return &sdk.Account{ID: jid.String(), Name: name}
}

func (d *Adapter) AuthStatus(ctx context.Context) (sdk.AuthState, error) {
	switch {
	case !d.app.wa.IsAuthed():
		return sdk.AuthState{State: sdk.StateAuthRequired}, nil
	case !d.app.wa.IsConnected():
		return sdk.AuthState{State: sdk.StateConnecting, Account: d.account()}, nil
	}
	return sdk.AuthState{State: sdk.StateConnected, Account: d.account()}, nil
}

// BeginAuth links a new device with a QR code. Each new code is a step.
func (d *Adapter) BeginAuth(ctx context.Context, kind string, steps sdk.StepSink) error {
	if kind != "" && kind != sdk.StepQR {
		return sdk.Invalid("the login kind must be qr")
	}
	if d.app.wa.IsAuthed() {
		return sdk.Invalid("the session is already logged in; log out first")
	}
	var stepErr error
	err := d.app.wa.Connect(ctx, wa.ConnectOptions{
		AllowQR: true,
		OnQR: func(code string, timeout time.Duration) {
			if stepErr != nil {
				return
			}
			stepErr = steps.Step(sdk.Step{
				Kind: sdk.StepQR, Data: code,
				Message:   "Open WhatsApp on your phone. Go to Linked devices and scan this code.",
				ExpiresAt: sdk.FormatTime(time.Now().Add(timeout)),
			})
		},
	})
	if err == nil && stepErr != nil {
		err = stepErr
	}
	if err != nil {
		d.app.wa.Close()
		return err
	}
	d.poke()
	return nil
}

func (d *Adapter) SubmitAuth(ctx context.Context, flowID string, fields map[string]string) error {
	return sdk.Invalid("the qr login takes no input")
}

func (d *Adapter) Logout(ctx context.Context) error {
	if !d.app.wa.IsAuthed() {
		return nil
	}
	if err := d.app.wa.Logout(ctx); err != nil {
		return wrapWA(err)
	}
	return nil
}

// enqueue receives whatsmeow events. It never blocks the whatsmeow loop.
func (d *Adapter) enqueue(evt any) {
	d.mu.Lock()
	d.queue = append(d.queue, evt)
	d.mu.Unlock()
	d.poke()
}

func (d *Adapter) poke() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *Adapter) drain() []any {
	d.mu.Lock()
	defer d.mu.Unlock()
	q := d.queue
	d.queue = nil
	return q
}

// Run holds the connection until ctx ends. It connects when the session is
// logged in, and reconnects with a backoff of up to one minute.
func (d *Adapter) Run(ctx context.Context, e sdk.Emitter) error {
	id := d.app.wa.AddEventHandler(d.enqueue)
	defer d.app.wa.RemoveEventHandler(id)
	backoff := time.Second
	var retry <-chan time.Time
	halted := false
	for {
		if !halted && retry == nil && d.app.wa.IsAuthed() && !d.app.wa.IsConnected() {
			if err := d.app.wa.Connect(ctx, wa.ConnectOptions{}); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				if serr := e.Status(sdk.Status{State: sdk.StateConnecting, Message: err.Error()}); serr != nil {
					return serr
				}
				retry = time.After(backoff)
				backoff = min(backoff*2, time.Minute)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-retry:
			retry = nil
		case <-d.wake:
		}
		for _, evt := range d.drain() {
			st, err := d.handle(ctx, e, evt)
			if err != nil {
				return err
			}
			switch st {
			case runConnected:
				backoff, halted = time.Second, false
			case runLost:
				if retry == nil {
					retry = time.After(backoff)
					backoff = min(backoff*2, time.Minute)
				}
			case runHalted:
				halted = true
			}
		}
	}
}

type runState int

const (
	runSame runState = iota
	runConnected
	runLost
	runHalted
)

// handle stores and emits one whatsmeow event.
func (d *Adapter) handle(ctx context.Context, e sdk.Emitter, evt any) (runState, error) {
	switch v := evt.(type) {
	case *events.Message:
		ev, ok := d.liveEvent(ctx, v)
		if ok {
			return runSame, e.Event(ev)
		}
	case *events.HistorySync:
		d.storeHistory(ctx, v)
	case *events.Connected:
		return runConnected, e.Status(sdk.Status{State: sdk.StateConnected, Account: d.account()})
	case *events.Disconnected:
		return runLost, e.Status(sdk.Status{State: sdk.StateConnecting, Message: "disconnected"})
	case *events.LoggedOut:
		return runHalted, e.Status(sdk.Status{State: sdk.StateAuthRequired, Reason: sdk.ReasonRevoked, Message: "the phone removed this linked device (" + v.Reason.String() + ")"})
	case *events.StreamReplaced:
		return runHalted, e.Status(sdk.Status{State: sdk.StateError, Message: "another client opened this WhatsApp session; restart the connector"})
	case *events.TemporaryBan:
		return runHalted, e.Status(sdk.Status{State: sdk.StateError, Message: fmt.Sprintf("WhatsApp banned this number for now (%s, ends in %s)", v.Code, v.Expire)})
	case *events.ClientOutdated:
		return runHalted, e.Status(sdk.Status{State: sdk.StateError, Message: "WhatsApp refused this client version; update the connector"})
	}
	return runSame, nil
}

func (d *Adapter) storeHistory(ctx context.Context, v *events.HistorySync) {
	for _, conv := range v.Data.GetConversations() {
		chatID := strings.TrimSpace(conv.GetID())
		if chatID == "" {
			continue
		}
		for _, m := range conv.GetMessages() {
			if m.GetMessage() == nil {
				continue
			}
			pm := wa.ParseHistoryMessage(chatID, m.GetMessage())
			if pm.ID == "" || pm.Chat.IsEmpty() {
				continue
			}
			if err := d.app.storeParsedMessage(ctx, pm); err != nil {
				slog.Warn("store history message", "id", pm.ID, "err", err)
			}
		}
	}
}

// liveEvent stores a live message and maps it. It skips status updates,
// newsletters, removed reactions, and messages with no content.
func (d *Adapter) liveEvent(ctx context.Context, v *events.Message) (sdk.Event, bool) {
	if isSKDMOnly(v) || v.Info.Chat.Server == types.BroadcastServer || v.Info.Chat.Server == types.NewsletterServer {
		return sdk.Event{}, false
	}
	pm := wa.ParseLiveMessage(v)
	if pm.ReactionToID != "" && pm.ReactionEmoji == "" && v.Message.GetEncReactionMessage() != nil {
		if r, err := d.app.wa.DecryptReaction(ctx, v); err == nil && r != nil {
			pm.ReactionEmoji = r.GetText()
		}
	}
	if err := d.app.storeParsedMessage(ctx, pm); err != nil {
		slog.Warn("store message", "id", pm.ID, "err", err)
	}
	sender := v.Info.Sender
	if sender.Server == types.HiddenUserServer && !v.Info.SenderAlt.IsEmpty() {
		sender = v.Info.SenderAlt
	}
	name := strings.TrimSpace(pm.PushName)
	if pm.FromMe {
		name = "me"
	}
	ev := sdk.Event{
		ID: pm.ID, TS: sdk.FormatTime(pm.Timestamp),
		Room:   d.room(ctx, pm.Chat, pm.PushName),
		Sender: &sdk.Author{ID: sender.ToNonAD().String(), Name: name, Self: pm.FromMe},
	}
	if raw, err := json.Marshal(v.Info); err == nil {
		ev.Raw = raw
	}
	if pm.ReactionToID != "" {
		if pm.ReactionEmoji == "" {
			return sdk.Event{}, false
		}
		ev.Type = "reaction"
		ev.Data, _ = json.Marshal(map[string]string{"emoji": pm.ReactionEmoji, "message_id": pm.ReactionToID}) //nolint:errcheck // strings always encode
		return ev, true
	}
	ev.Type = "message"
	ev.Text = pm.Text
	ev.ReplyTo = pm.ReplyToID
	if s := strings.TrimSpace(pm.PushName); s != "" && !pm.FromMe {
		ev.Data, _ = json.Marshal(map[string]string{"push_name": s}) //nolint:errcheck // strings always encode
	}
	if pm.Media != nil {
		if pm.Media.Caption != "" && ev.Text == "" {
			ev.Text = pm.Media.Caption
		}
		ev.Attachments = []sdk.Attachment{d.download(ctx, pm)}
	}
	if ev.Text == "" && len(ev.Attachments) == 0 {
		return sdk.Event{}, false
	}
	return ev, true
}

// download saves inbound media and returns the attachment. A failed or too
// large download gives an attachment with no path.
func (d *Adapter) download(ctx context.Context, pm wa.ParsedMessage) sdk.Attachment {
	at := sdk.Attachment{ID: pm.ID, Name: pm.Media.Filename, Mime: pm.Media.MimeType, Size: int64(pm.Media.FileLength)}
	if pm.Media.FileLength > maxDownload {
		return at
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	job := mediaJob{chatJID: pm.Chat.String(), msgID: pm.ID}
	if err := d.app.downloadMediaJob(ctx, job); err != nil {
		slog.Warn("download media", "id", pm.ID, "err", err)
		return at
	}
	if info, err := d.app.db.GetMediaDownloadInfo(job.chatJID, job.msgID); err == nil {
		at.Path = info.LocalPath
		if at.Name == "" && info.LocalPath != "" {
			at.Name = path.Base(info.LocalPath)
		}
	}
	return at
}

func (d *Adapter) room(ctx context.Context, chat types.JID, pushName string) *sdk.Room {
	kind := sdk.RoomDM
	if chat.Server == types.GroupServer {
		kind = sdk.RoomGroup
	}
	return &sdk.Room{ID: chat.String(), Name: d.app.wa.ResolveChatName(ctx, chat, pushName), Kind: kind}
}

// Send sends text, a reply, or files. The text is the caption of the first
// file.
func (d *Adapter) Send(ctx context.Context, m sdk.SendParams) (sdk.SendResult, error) {
	to, err := d.jid(m.Room)
	if err != nil {
		return sdk.SendResult{}, err
	}
	reply, err := d.quote(m.Room, m.ReplyTo)
	if err != nil {
		return sdk.SendResult{}, err
	}
	if len(m.Attachments) > 0 {
		var first string
		for i, at := range m.Attachments {
			f, err := d.outFile(ctx, at)
			if err != nil {
				return sdk.SendResult{}, err
			}
			if i == 0 {
				f.Caption, f.Reply = m.Text, reply
			}
			id, _, err := d.app.SendFile(ctx, to, f)
			if err != nil {
				return sdk.SendResult{}, wrapWA(err)
			}
			if i == 0 {
				first = id
			}
		}
		return sdk.SendResult{MessageID: first}, nil
	}
	msg := &waProto.Message{Conversation: proto.String(m.Text)}
	if reply != nil {
		msg = &waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{Text: proto.String(m.Text), ContextInfo: reply}}
	}
	id, err := d.app.wa.SendProtoMessage(ctx, to, msg)
	if err != nil {
		return sdk.SendResult{}, wrapWA(err)
	}
	now := time.Now().UTC()
	name := d.app.wa.ResolveChatName(ctx, to, "")
	if err := d.app.db.UpsertChat(to.String(), chatKind(to), name, now); err != nil {
		slog.Warn("store chat", "err", err)
	}
	if err := d.app.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID: to.String(), ChatName: name, MsgID: id, SenderName: "me",
		Timestamp: now, FromMe: true, Text: m.Text, DisplayText: m.Text,
	}); err != nil {
		slog.Warn("store sent message", "err", err)
	}
	return sdk.SendResult{MessageID: id}, nil
}

// quote builds the reply context of message id in room.
func (d *Adapter) quote(room, id string) (*waProto.ContextInfo, error) {
	if id == "" {
		return nil, nil
	}
	orig, err := d.app.db.GetMessage(room, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sdk.NotFound("reply_to: no message " + id + " in the room")
		}
		return nil, err
	}
	ci := &waProto.ContextInfo{
		StanzaID:      proto.String(id),
		QuotedMessage: &waProto.Message{Conversation: proto.String(orig.Text)},
	}
	if p := d.author(orig); !p.IsEmpty() {
		ci.Participant = proto.String(p.String())
	}
	return ci, nil
}

// outFile loads an outbound attachment from its path or its URL.
func (d *Adapter) outFile(ctx context.Context, at sdk.Attachment) (OutFile, error) {
	f := OutFile{Path: at.Path, Name: at.Name, Mime: at.Mime}
	if at.Path != "" {
		return f, nil
	}
	if at.URL == "" {
		return f, sdk.Invalid("an attachment needs a path or a url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, at.URL, nil)
	if err != nil {
		return f, sdk.Invalid("attachment url: " + err.Error())
	}
	resp, err := d.fetch.Do(req)
	if err != nil {
		return f, sdk.Transient("download attachment: " + err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return f, sdk.Permanent("download attachment: HTTP " + strconv.Itoa(resp.StatusCode))
	}
	if f.Data, err = io.ReadAll(io.LimitReader(resp.Body, maxDownload+1)); err != nil {
		return f, sdk.Transient("download attachment: " + err.Error())
	}
	if len(f.Data) > maxDownload {
		return f, sdk.Invalid("the attachment is larger than 100 MiB")
	}
	if f.Name == "" {
		f.Name = path.Base(req.URL.Path)
	}
	f.Path = f.Name
	if f.Mime == "" {
		f.Mime = resp.Header.Get("Content-Type")
	}
	return f, nil
}

// History pages older messages, newest page first. before is a message id.
func (d *Adapter) History(ctx context.Context, q sdk.HistoryQuery) (sdk.EventPage, error) {
	if _, err := d.jid(q.Room); err != nil {
		return sdk.EventPage{}, err
	}
	p := store.PageParams{ChatJID: q.Room, Limit: q.Limit}
	if q.Before != "" {
		m, err := d.app.db.GetMessage(q.Room, q.Before)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return sdk.EventPage{}, sdk.NotFound("before: no message " + q.Before + " in the room")
			}
			return sdk.EventPage{}, err
		}
		p.Before = &store.Cursor{TS: m.Timestamp, ID: m.MsgID}
	} else {
		p.Before = &store.Cursor{TS: time.Now().Add(time.Hour), ID: ""}
	}
	msgs, err := d.app.db.Page(p)
	if err != nil {
		return sdk.EventPage{}, err
	}
	page := sdk.EventPage{Events: []sdk.Event{}}
	for i := len(msgs) - 1; i >= 0; i-- {
		page.Events = append(page.Events, d.storedEvent(msgs[i]))
	}
	if q.Limit > 0 && len(msgs) == q.Limit {
		page.Next = msgs[len(msgs)-1].MsgID
	}
	return page, nil
}

// Replay returns the stored messages after the event id after. An unknown
// id means the cursor is gone.
func (d *Adapter) Replay(ctx context.Context, after string, limit int) ([]sdk.Event, bool, error) {
	m, err := d.app.db.FindMessage(after)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	msgs, err := d.app.db.Page(store.PageParams{After: &store.Cursor{TS: m.Timestamp, ID: m.MsgID}, Limit: limit})
	if err != nil {
		return nil, false, err
	}
	out := []sdk.Event{}
	for _, m := range msgs {
		out = append(out, d.storedEvent(m))
	}
	return out, true, nil
}

func (d *Adapter) storedEvent(m store.Message) sdk.Event {
	kind := sdk.RoomDM
	if strings.HasSuffix(m.ChatJID, "@"+types.GroupServer) {
		kind = sdk.RoomGroup
	}
	sender := &sdk.Author{ID: m.SenderJID, Name: m.SenderName, Self: m.FromMe}
	if sender.ID == "" && !m.FromMe {
		sender.ID = m.ChatJID
	}
	if m.FromMe {
		if acct := d.account(); acct != nil {
			sender.ID = acct.ID
		}
	}
	ev := sdk.Event{
		ID: m.MsgID, Type: "message", TS: sdk.FormatTime(m.Timestamp),
		Room:   &sdk.Room{ID: m.ChatJID, Name: m.ChatName, Kind: kind},
		Sender: sender, Text: m.Text,
	}
	if m.MediaType != "" {
		ev.Attachments = []sdk.Attachment{{ID: m.MsgID, Name: m.Filename, Mime: m.MimeType, Size: int64(m.FileLength), Path: m.LocalPath}}
	}
	return ev
}

// ListRooms lists the known chats, newest first. The cursor is an offset.
func (d *Adapter) ListRooms(ctx context.Context, q sdk.RoomQuery) (sdk.RoomPage, error) {
	off := 0
	if q.Cursor != "" {
		n, err := strconv.Atoi(q.Cursor)
		if err != nil || n < 0 {
			return sdk.RoomPage{}, sdk.Invalid("bad cursor")
		}
		off = n
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	chats, err := d.app.db.ListChats(q.Query, 100000)
	if err != nil {
		return sdk.RoomPage{}, err
	}
	var rooms []sdk.Room
	for _, c := range chats {
		r := chatRoom(c)
		if r.Kind == "" || (q.Kind != "" && q.Kind != r.Kind) {
			continue
		}
		rooms = append(rooms, r)
	}
	page := sdk.RoomPage{Rooms: []sdk.Room{}}
	if off < len(rooms) {
		end := min(off+limit, len(rooms))
		page.Rooms = rooms[off:end]
		if end < len(rooms) {
			page.Next = strconv.Itoa(end)
		}
	}
	return page, nil
}

func (d *Adapter) GetRoom(ctx context.Context, id string) (sdk.Room, error) {
	if _, err := d.jid(id); err != nil {
		return sdk.Room{}, err
	}
	c, err := d.app.db.GetChat(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sdk.Room{}, sdk.NotFound("no room " + id)
		}
		return sdk.Room{}, err
	}
	return chatRoom(c), nil
}

func chatRoom(c store.Chat) sdk.Room {
	r := sdk.Room{ID: c.JID, Name: c.Name}
	switch c.Kind {
	case "dm":
		r.Kind = sdk.RoomDM
	case "group":
		r.Kind = sdk.RoomGroup
	}
	if !c.LastMessageTS.IsZero() {
		r.LastActivityAt = sdk.FormatTime(c.LastMessageTS)
	}
	return r
}

// OpenRoom returns the DM room of a phone number or a user JID.
func (d *Adapter) OpenRoom(ctx context.Context, user string) (sdk.Room, error) {
	if !strings.Contains(user, "@") {
		user = strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, user)
	}
	jid, err := wa.ParseUserOrJID(user)
	if err != nil || user == "" || jid.Server != types.DefaultUserServer {
		return sdk.Room{}, sdk.Invalid("user must be a phone number or a user JID")
	}
	jid = jid.ToNonAD()
	name := d.app.wa.ResolveChatName(ctx, jid, "")
	if err := d.app.db.UpsertChat(jid.String(), "dm", name, time.Time{}); err != nil {
		return sdk.Room{}, err
	}
	return sdk.Room{ID: jid.String(), Name: name, Kind: sdk.RoomDM}, nil
}

func (d *Adapter) Typing(ctx context.Context, room, thread string) error {
	to, err := d.jid(room)
	if err != nil {
		return err
	}
	return wrapWA(d.app.wa.SendChatPresence(ctx, to, types.ChatPresenceComposing, types.ChatPresenceMediaText))
}

// Seen sends a read receipt.
func (d *Adapter) Seen(ctx context.Context, room, msgID string) error {
	chat, m, err := d.message(room, msgID)
	if err != nil {
		return err
	}
	return wrapWA(d.app.wa.MarkRead(ctx, []types.MessageID{msgID}, time.Now(), chat, d.author(m)))
}

func (d *Adapter) React(ctx context.Context, room, msgID, emoji string) error {
	chat, m, err := d.message(room, msgID)
	if err != nil {
		return err
	}
	_, err = d.app.wa.SendReaction(ctx, chat, d.author(m), msgID, emoji)
	return wrapWA(err)
}

// message reads a stored message of room.
func (d *Adapter) message(room, msgID string) (types.JID, store.Message, error) {
	chat, err := d.jid(room)
	if err != nil {
		return chat, store.Message{}, err
	}
	m, err := d.app.db.GetMessage(room, msgID)
	if errors.Is(err, sql.ErrNoRows) {
		return chat, m, sdk.NotFound("no message " + msgID + " in the room")
	}
	return chat, m, err
}

// author is the JID that wrote m: the sender, the account for an own
// message, or the chat for a DM.
func (d *Adapter) author(m store.Message) types.JID {
	if m.FromMe {
		jid, _ := d.app.wa.Account()
		return jid
	}
	if jid, err := types.ParseJID(m.SenderJID); err == nil && !jid.IsEmpty() {
		return jid.ToNonAD()
	}
	jid, _ := types.ParseJID(m.ChatJID)
	return jid
}

func (d *Adapter) jid(room string) (types.JID, error) {
	jid, err := types.ParseJID(room)
	if err != nil || jid.IsEmpty() || (jid.Server != types.DefaultUserServer && jid.Server != types.GroupServer && jid.Server != types.HiddenUserServer) {
		return types.JID{}, sdk.Invalid("room must be a WhatsApp user or group JID")
	}
	return jid, nil
}

// wrapWA maps a client error to a protocol error. A missing connection is
// transient.
func wrapWA(err error) error {
	if err == nil {
		return nil
	}
	if sdk.CodeOf(err) == "" && strings.Contains(err.Error(), "not connected") {
		return sdk.Transient(err.Error())
	}
	return err
}
