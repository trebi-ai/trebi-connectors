// Package fakewa is a WhatsApp client with no network. The app tests use
// New. `serve --sandbox` uses Sandbox, which adds fixed chats, a QR login
// that it keeps in the state folder, and an echo of each sent message.
package fakewa

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/wa"
)

// SessionFile is the sandbox login in the state folder.
const SessionFile = "fakewa-session.json"

// Fixed JIDs of the sandbox.
var (
	Me    = types.JID{User: "5511000000000", Server: types.DefaultUserServer}
	Ana   = types.JID{User: "5511911110001", Server: types.DefaultUserServer}
	Bruno = types.JID{User: "5511911110002", Server: types.DefaultUserServer}
	Group = types.JID{User: "120363000000000099", Server: types.GroupServer}
)

// Client implements app.WAClient in memory.
type Client struct {
	mu sync.Mutex

	authed    bool
	connected bool

	nextHandlerID uint32
	handlers      map[uint32]func(interface{})

	connectEvents []interface{}

	contacts map[types.JID]types.ContactInfo
	groups   map[types.JID]*types.GroupInfo
	names    map[types.JID]string // the RememberGroup cache

	onDemandHistory func(lastKnown types.MessageInfo, count int) *events.HistorySync

	sent       []*waProto.Message
	groupCalls int
	reactions  []string
	reads      []string
	typing     []string

	// Sandbox only.
	session   string // session file; "" keeps the login in memory
	echo      bool
	scanDelay time.Duration
	seq       int
	uploads   map[string][]byte
}

// New returns a logged-in client with no chats.
func New() *Client {
	return &Client{
		authed:        true,
		handlers:      map[uint32]func(interface{}){},
		contacts:      map[types.JID]types.ContactInfo{},
		groups:        map[types.JID]*types.GroupInfo{},
		names:         map[types.JID]string{},
		nextHandlerID: 1,
		uploads:       map[string][]byte{},
	}
}

type sessionState struct {
	Authed bool `json:"authed"`
	Seq    int  `json:"seq"`
}

// Sandbox returns the client of `serve --sandbox`. The login and the
// message counter persist in stateDir. An empty stateDir starts logged
// out. A QR login completes after a short scan delay.
func Sandbox(stateDir string) (*Client, error) {
	f := New()
	f.authed = false
	f.echo = true
	f.scanDelay = 300 * time.Millisecond
	f.session = filepath.Join(stateDir, SessionFile)
	data, err := os.ReadFile(f.session)
	switch {
	case err == nil:
		var s sessionState
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("read %s: %w", SessionFile, err)
		}
		f.authed, f.seq = s.Authed, s.Seq
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	f.contacts[Ana] = types.ContactInfo{Found: true, FullName: "Ana Souza", FirstName: "Ana", PushName: "Ana"}
	f.contacts[Bruno] = types.ContactInfo{Found: true, FullName: "Bruno Lima", FirstName: "Bruno", PushName: "Bruno"}
	f.groups[Group] = &types.GroupInfo{
		JID:          Group,
		GroupName:    types.GroupName{Name: "Sandbox group"},
		Participants: []types.GroupParticipant{{JID: Me}, {JID: Ana}, {JID: Bruno}},
	}
	f.connectEvents = []interface{}{sandboxHistory()}
	return f, nil
}

// sandboxHistory is the history sync that the phone sends on connect.
func sandboxHistory() *events.HistorySync {
	ts := uint64(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC).Unix())
	msg := func(chat types.JID, id, participant, text string, at uint64) *waHistorySync.HistorySyncMsg {
		key := &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), FromMe: proto.Bool(false), ID: proto.String(id)}
		if participant != "" {
			key.Participant = proto.String(participant)
		}
		return &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{
			Key: key, MessageTimestamp: proto.Uint64(at),
			Message: &waProto.Message{Conversation: proto.String(text)},
		}}
	}
	return &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_RECENT.Enum(),
		Conversations: []*waHistorySync.Conversation{
			{ID: proto.String(Ana.String()), Messages: []*waHistorySync.HistorySyncMsg{msg(Ana, "SANDBOXANA1", "", "Welcome to the WhatsApp sandbox.", ts)}},
			{ID: proto.String(Group.String()), Messages: []*waHistorySync.HistorySyncMsg{msg(Group, "SANDBOXGRP1", Bruno.String(), "Hello, group.", ts+60)}},
		},
	}}
}

// save writes the sandbox login. Call it with f.mu held.
func (f *Client) save() error {
	if f.session == "" {
		return nil
	}
	data, err := json.Marshal(sessionState{Authed: f.authed, Seq: f.seq})
	if err != nil {
		return err
	}
	tmp := f.session + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.session)
}

// SetAuthed sets the login state.
func (f *Client) SetAuthed(v bool) { f.mu.Lock(); f.authed = v; f.mu.Unlock() }

// AddContact adds a contact to the address book.
func (f *Client) AddContact(jid types.JID, info types.ContactInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.contacts[jid] = info
}

// AddGroup adds a joined group.
func (f *Client) AddGroup(g *types.GroupInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groups[g.JID] = g
}

// GroupInfoCalls returns the number of GetGroupInfo calls.
func (f *Client) GroupInfoCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.groupCalls
}

// SetConnectEvents sets the events that each Connect emits.
func (f *Client) SetConnectEvents(evts ...interface{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectEvents = evts
}

// SetOnDemandHistory sets the answer to RequestHistorySyncOnDemand.
func (f *Client) SetOnDemandHistory(fn func(lastKnown types.MessageInfo, count int) *events.HistorySync) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onDemandHistory = fn
}

// Sent returns the sent messages.
func (f *Client) Sent() []*waProto.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*waProto.Message(nil), f.sent...)
}

// Reactions returns the sent reactions as "chat|sender|id|emoji".
func (f *Client) Reactions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reactions...)
}

// Reads returns the read receipts as "chat|sender|id".
func (f *Client) Reads() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reads...)
}

// Typing returns the chat presences as "chat|state".
func (f *Client) Typing() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.typing...)
}

// Emit sends evt to each event handler.
func (f *Client) Emit(evt interface{}) {
	f.mu.Lock()
	handlers := make([]func(interface{}), 0, len(f.handlers))
	for _, h := range f.handlers {
		handlers = append(handlers, h)
	}
	f.mu.Unlock()
	for _, h := range handlers {
		h(evt)
	}
}

func (f *Client) Close() { f.mu.Lock(); f.connected = false; f.mu.Unlock() }

func (f *Client) CloseStore() error { f.Close(); return nil }

func (f *Client) IsAuthed() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.authed }

func (f *Client) IsConnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

func (f *Client) Connect(ctx context.Context, opts wa.ConnectOptions) error {
	f.mu.Lock()
	authed := f.authed
	delay := f.scanDelay
	eventsToEmit := append([]interface{}{}, f.connectEvents...)
	f.mu.Unlock()

	if !authed && !opts.AllowQR {
		return fmt.Errorf("not authenticated; run `whatsapp-cli auth`")
	}
	if !authed {
		switch {
		case opts.OnQR != nil:
			opts.OnQR("qr-code-1", 20*time.Second)
		case opts.OnQRCode != nil:
			opts.OnQRCode("qr-code-1")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		f.mu.Lock()
		f.authed = true
		err := f.save()
		f.mu.Unlock()
		if err != nil {
			return err
		}
	}
	f.mu.Lock()
	f.connected = true
	f.mu.Unlock()
	f.Emit(&events.Connected{})
	for _, e := range eventsToEmit {
		f.Emit(e)
	}
	return nil
}

func (f *Client) AddEventHandler(handler func(interface{})) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextHandlerID
	f.nextHandlerID++
	f.handlers[id] = handler
	return id
}

func (f *Client) RemoveEventHandler(id uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.handlers, id)
}

// ResolveChatName follows wa.Client: the group name for a group, or the
// contact name and then the push name for a DM. It returns "" when the name
// is not known.
func (f *Client) ResolveChatName(ctx context.Context, chat types.JID, pushName string) string {
	if chat.Server == types.GroupServer {
		f.mu.Lock()
		defer f.mu.Unlock()
		if name := f.names[chat]; name != "" {
			return name
		}
		if gi := f.groups[chat]; gi != nil {
			return gi.GroupName.Name
		}
		return ""
	}
	if info, _ := f.GetContact(ctx, chat.ToNonAD()); info.Found {
		if name := wa.BestContactName(info); name != "" {
			return name
		}
	}
	if pushName != "-" {
		return pushName
	}
	return ""
}

// KnownChatName is ResolveChatName. The fake has no network.
func (f *Client) KnownChatName(ctx context.Context, chat types.JID, pushName string) string {
	return f.ResolveChatName(ctx, chat, pushName)
}

func (f *Client) RememberGroup(jid types.JID, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.names[jid] = name
}

func (f *Client) GetContact(ctx context.Context, jid types.JID) (types.ContactInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.contacts[jid]; ok {
		return v, nil
	}
	return types.ContactInfo{Found: false}, nil
}

func (f *Client) GetAllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[types.JID]types.ContactInfo, len(f.contacts))
	for k, v := range f.contacts {
		out[k] = v
	}
	return out, nil
}

func (f *Client) GetJoinedGroups(ctx context.Context) ([]*types.GroupInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*types.GroupInfo, 0, len(f.groups))
	for _, g := range f.groups {
		out = append(out, g)
	}
	return out, nil
}

func (f *Client) GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groupCalls++
	return f.groups[jid], nil
}

func (f *Client) SetGroupName(ctx context.Context, jid types.JID, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.groups[jid]
	if g == nil {
		g = &types.GroupInfo{JID: jid}
		f.groups[jid] = g
	}
	g.GroupName.Name = name
	return nil
}

func (f *Client) UpdateGroupParticipants(ctx context.Context, group types.JID, users []types.JID, action wa.GroupParticipantAction) ([]types.GroupParticipant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.groups[group]
	if g == nil {
		g = &types.GroupInfo{JID: group}
		f.groups[group] = g
	}
	switch action {
	case wa.GroupParticipantAdd:
		for _, u := range users {
			g.Participants = append(g.Participants, types.GroupParticipant{JID: u})
		}
	case wa.GroupParticipantRemove:
		var kept []types.GroupParticipant
		rm := map[types.JID]bool{}
		for _, u := range users {
			rm[u] = true
		}
		for _, p := range g.Participants {
			if !rm[p.JID] {
				kept = append(kept, p)
			}
		}
		g.Participants = kept
	default:
		// promote and demote change nothing here
	}
	return g.Participants, nil
}

func (f *Client) GetGroupInviteLink(ctx context.Context, group types.JID, reset bool) (string, error) {
	return "https://chat.whatsapp.com/invite/test", nil
}

func (f *Client) JoinGroupWithLink(ctx context.Context, code string) (types.JID, error) {
	return types.ParseJID("12345@g.us")
}

func (f *Client) LeaveGroup(ctx context.Context, group types.JID) error { return nil }

func (f *Client) SendText(ctx context.Context, to types.JID, text string) (types.MessageID, error) {
	return f.SendProtoMessage(ctx, to, &waProto.Message{Conversation: proto.String(text)})
}

func (f *Client) SendChatPresence(ctx context.Context, to types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typing = append(f.typing, to.String()+"|"+string(state))
	return nil
}

func (f *Client) MarkRead(ctx context.Context, ids []types.MessageID, timestamp time.Time, chat, sender types.JID, receiptType ...types.ReceiptType) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		f.reads = append(f.reads, chat.String()+"|"+sender.String()+"|"+string(id))
	}
	return nil
}

func (f *Client) SendPresence(ctx context.Context, state types.Presence) error {
	return nil
}

// SendProtoMessage records msg and returns "S<n>". The sandbox also emits
// the message as an own live message, as a second device of the account
// does.
func (f *Client) SendProtoMessage(ctx context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error) {
	f.mu.Lock()
	if !f.connected && f.echo {
		f.mu.Unlock()
		return "", errors.New("not connected")
	}
	f.sent = append(f.sent, msg)
	f.seq++
	id := types.MessageID(fmt.Sprintf("S%d", f.seq))
	err := f.save()
	echo := f.echo
	f.mu.Unlock()
	if err != nil {
		return "", err
	}
	if echo {
		f.Emit(&events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: to, Sender: Me, IsFromMe: true, IsGroup: to.Server == types.GroupServer},
				ID:            id, Timestamp: time.Now(),
			},
			Message: msg,
		})
	}
	return id, nil
}

// Upload keeps data in memory. DownloadMediaToFile returns it.
func (f *Client) Upload(ctx context.Context, data []byte, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	key := make([]byte, 32)
	_, _ = rand.Read(key) //nolint:errcheck // crypto/rand does not fail
	path := "/fake/" + hex.EncodeToString(key[:8])
	f.mu.Lock()
	f.uploads[path] = append([]byte(nil), data...)
	f.mu.Unlock()
	return whatsmeow.UploadResponse{DirectPath: path, MediaKey: key, FileLength: uint64(len(data))}, nil
}

func (f *Client) DecryptReaction(ctx context.Context, reaction *events.Message) (*waProto.ReactionMessage, error) {
	return nil, fmt.Errorf("not supported")
}

// DownloadMediaToFile writes the uploaded bytes of directPath, or "test".
func (f *Client) DownloadMediaToFile(ctx context.Context, directPath string, encFileHash, fileHash, mediaKey []byte, fileLength uint64, mediaType, mmsType string, targetPath string) (int64, error) {
	f.mu.Lock()
	data, ok := f.uploads[directPath]
	f.mu.Unlock()
	if !ok {
		data = []byte("test")
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		return 0, err
	}
	if err := os.WriteFile(targetPath, data, 0o600); err != nil {
		return 0, err
	}
	return int64(len(data)), nil
}

func (f *Client) RequestHistorySyncOnDemand(ctx context.Context, lastKnown types.MessageInfo, count int) (types.MessageID, error) {
	f.mu.Lock()
	cb := f.onDemandHistory
	f.mu.Unlock()
	if cb != nil {
		f.Emit(cb(lastKnown, count))
	}
	return types.MessageID("req"), nil
}

func (f *Client) Logout(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authed, f.connected = false, false
	return f.save()
}

func (f *Client) Account() (types.JID, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.authed {
		return types.JID{}, ""
	}
	return Me, "Me"
}

func (f *Client) SendReaction(ctx context.Context, chat, sender types.JID, id types.MessageID, emoji string) (types.MessageID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactions = append(f.reactions, chat.String()+"|"+sender.String()+"|"+string(id)+"|"+emoji)
	return "R" + id, nil
}
