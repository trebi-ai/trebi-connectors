package wa

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mdp/qrterminal/v3"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

type Options struct {
	StorePath string
}

type Client struct {
	opts Options

	mu        sync.Mutex
	client    *whatsmeow.Client
	db        *sql.DB // session.db
	container *sqlstore.Container
	log       waLog.Logger

	// Handlers live here, not on client, so they survive a renew.
	hmu      sync.Mutex
	handlers map[uint32]func(interface{})
	nextID   uint32
}

func New(opts Options) (*Client, error) {
	if strings.TrimSpace(opts.StorePath) == "" {
		return nil, fmt.Errorf("StorePath is required")
	}
	c := &Client{opts: opts}
	if err := c.init(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) init() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx := context.Background()
	// Stdout carries JSON output and the serve protocol, so logs go to stderr.
	base := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true, TimeFormat: time.RFC3339}).With().Timestamp().Logger()
	dbLog := waLog.Zerolog(base.Level(zerolog.ErrorLevel).With().Str("module", "Database").Logger())
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL", c.opts.StorePath))
	if err != nil {
		return fmt.Errorf("open whatsmeow store: %w", err)
	}
	container := sqlstore.NewWithDB(db, "sqlite3", dbLog)
	if err := container.Upgrade(ctx); err != nil {
		_ = db.Close()
		return fmt.Errorf("open whatsmeow store: %w", err)
	}
	c.db, c.container = db, container
	c.log = waLog.Zerolog(base.Level(zerolog.WarnLevel).With().Str("module", "Client").Logger())

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			deviceStore = container.NewDevice()
		} else {
			return fmt.Errorf("get device store: %w", err)
		}
	}
	c.setDevice(deviceStore)
	return nil
}

// setDevice makes a whatsmeow client for d. The caller holds c.mu.
func (c *Client) setDevice(d *store.Device) {
	c.client = whatsmeow.NewClient(d, c.log)
	// App owns reconnect (listen/sync) so we can apply backoff + max-reconnect.
	c.client.EnableAutoReconnect = false
	c.client.AddEventHandler(c.dispatch)
}

// renewIfDeleted gives a logged-out client a new empty device, so that a new
// login can start. whatsmeow never reuses a deleted device.
func (c *Client) renewIfDeleted() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil || !c.client.Store.Deleted {
		return
	}
	c.client.Disconnect()
	c.setDevice(c.container.NewDevice())
}

func (c *Client) dispatch(evt interface{}) {
	c.hmu.Lock()
	hs := make([]func(interface{}), 0, len(c.handlers))
	for _, h := range c.handlers {
		hs = append(hs, h)
	}
	c.hmu.Unlock()
	for _, h := range hs {
		h(evt)
	}
}

// Close disconnects. The client can connect again.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		c.client.Disconnect()
	}
}

// CloseStore disconnects, moves the WAL into session.db, and closes it.
// The client is not usable after it.
func (c *Client) CloseStore() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		c.client.Disconnect()
	}
	if c.db == nil {
		return nil
	}
	_, err := c.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	if cerr := c.container.Close(); err == nil {
		err = cerr
	}
	c.db = nil
	return err
}

func (c *Client) IsAuthed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client != nil && c.client.Store != nil && c.client.Store.ID != nil
}

func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client != nil && c.client.IsConnected()
}

type ConnectOptions struct {
	AllowQR  bool
	OnQRCode func(code string)
	// OnQR replaces OnQRCode. timeout is how long the code is valid.
	OnQR func(code string, timeout time.Duration)
}

func (c *Client) Connect(ctx context.Context, opts ConnectOptions) error {
	c.renewIfDeleted()
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil {
		return fmt.Errorf("whatsapp client is not initialized")
	}

	if cli.IsConnected() {
		return nil
	}

	authed := cli.Store != nil && cli.Store.ID != nil
	if !authed && !opts.AllowQR {
		return fmt.Errorf("not authenticated; run `whatsapp-cli auth`")
	}

	var qrChan <-chan whatsmeow.QRChannelItem
	if !authed {
		ch, _ := cli.GetQRChannel(ctx)
		qrChan = ch
	}

	// whatsmeow keeps this context for the life of the connection, also for
	// the reconnect after pairing. The caller's cancel must not end it.
	if err := cli.ConnectContext(context.WithoutCancel(ctx)); err != nil {
		return err
	}

	if authed {
		return nil
	}

	// Wait for QR flow to succeed or fail.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt, ok := <-qrChan:
			if !ok {
				return fmt.Errorf("QR channel closed")
			}
			switch evt.Event {
			case "code":
				if opts.OnQR != nil {
					opts.OnQR(evt.Code, evt.Timeout)
				} else if opts.OnQRCode != nil {
					opts.OnQRCode(evt.Code)
				} else {
					qrterminal.GenerateHalfBlock(evt.Code, qrterminal.M, os.Stdout)
				}
			case "success":
				return nil
			case "timeout":
				return fmt.Errorf("QR code timed out")
			case "error":
				return fmt.Errorf("QR error")
			}
		}
	}
}

func (c *Client) AddEventHandler(handler func(interface{})) uint32 {
	c.hmu.Lock()
	defer c.hmu.Unlock()
	if c.handlers == nil {
		c.handlers = map[uint32]func(interface{}){}
	}
	c.nextID++
	c.handlers[c.nextID] = handler
	return c.nextID
}

func (c *Client) RemoveEventHandler(id uint32) {
	c.hmu.Lock()
	defer c.hmu.Unlock()
	delete(c.handlers, id)
}

func (c *Client) SendText(ctx context.Context, to types.JID, text string) (types.MessageID, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	msg := &waProto.Message{Conversation: &text}
	resp, err := cli.SendMessage(ctx, to, msg)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// SendChatPresence updates typing/recording status in a chat (composing/paused).
// media is types.ChatPresenceMediaText ("") or types.ChatPresenceMediaAudio ("audio").
func (c *Client) SendChatPresence(ctx context.Context, to types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	return cli.SendChatPresence(ctx, to, state, media)
}

// MarkRead sends a read/played receipt for the given message IDs.
// sender must be set for group chats (the user who sent the messages).
// receiptType defaults to types.ReceiptTypeRead when omitted.
func (c *Client) MarkRead(ctx context.Context, ids []types.MessageID, timestamp time.Time, chat, sender types.JID, receiptType ...types.ReceiptType) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	return cli.MarkRead(ctx, ids, timestamp, chat, sender, receiptType...)
}

// SendPresence updates global online/offline presence (available/unavailable).
// available enables active delivery receipts (two gray checks on receive).
func (c *Client) SendPresence(ctx context.Context, state types.Presence) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	return cli.SendPresence(ctx, state)
}

func (c *Client) SendProtoMessage(ctx context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	resp, err := cli.SendMessage(ctx, to, msg)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (c *Client) Upload(ctx context.Context, data []byte, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return whatsmeow.UploadResponse{}, fmt.Errorf("not connected")
	}
	return cli.Upload(ctx, data, mediaType)
}

func (c *Client) DecryptReaction(ctx context.Context, reaction *events.Message) (*waProto.ReactionMessage, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}
	return cli.DecryptReaction(ctx, reaction)
}

func (c *Client) RequestHistorySyncOnDemand(ctx context.Context, lastKnown types.MessageInfo, count int) (types.MessageID, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	if count <= 0 {
		count = 50
	}
	if lastKnown.Chat.IsEmpty() || strings.TrimSpace(string(lastKnown.ID)) == "" || lastKnown.Timestamp.IsZero() {
		return "", fmt.Errorf("invalid last known message info")
	}

	ownID := types.JID{}
	if cli.Store != nil && cli.Store.ID != nil {
		ownID = cli.Store.ID.ToNonAD()
	}
	if ownID.IsEmpty() {
		return "", fmt.Errorf("not authenticated; run `whatsapp-cli auth`")
	}

	msg := cli.BuildHistorySyncRequest(&lastKnown, count)
	resp, err := cli.SendMessage(ctx, ownID, msg, whatsmeow.SendRequestExtra{Peer: true})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func ParseUserOrJID(s string) (types.JID, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return types.JID{}, fmt.Errorf("recipient is required")
	}
	if strings.Contains(s, "@") {
		return types.ParseJID(s)
	}
	return types.JID{User: s, Server: types.DefaultUserServer}, nil
}

func IsGroupJID(jid types.JID) bool {
	return jid.Server == types.GroupServer
}

func (c *Client) GetContact(ctx context.Context, jid types.JID) (types.ContactInfo, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || cli.Store == nil || cli.Store.Contacts == nil {
		return types.ContactInfo{}, fmt.Errorf("contacts store not available")
	}
	return cli.Store.Contacts.GetContact(ctx, jid)
}

func (c *Client) GetAllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || cli.Store == nil || cli.Store.Contacts == nil {
		return nil, fmt.Errorf("contacts store not available")
	}
	return cli.Store.Contacts.GetAllContacts(ctx)
}

func BestContactName(info types.ContactInfo) string {
	if !info.Found {
		return ""
	}
	if s := strings.TrimSpace(info.FullName); s != "" {
		return s
	}
	if s := strings.TrimSpace(info.FirstName); s != "" {
		return s
	}
	if s := strings.TrimSpace(info.BusinessName); s != "" {
		return s
	}
	if s := strings.TrimSpace(info.PushName); s != "" && s != "-" {
		return s
	}
	if s := strings.TrimSpace(info.RedactedPhone); s != "" {
		return s
	}
	return ""
}

func (c *Client) ResolveChatName(ctx context.Context, chat types.JID, pushName string) string {
	fallback := chat.String()

	if chat.Server == types.GroupServer || chat.IsBroadcastList() {
		info, err := c.GetGroupInfo(ctx, chat)
		if err == nil && info != nil {
			if name := strings.TrimSpace(info.GroupName.Name); name != "" {
				return name
			}
		}
	} else {
		info, err := c.GetContact(ctx, chat.ToNonAD())
		if err == nil {
			if name := BestContactName(info); name != "" {
				return name
			}
		}
	}

	if name := strings.TrimSpace(pushName); name != "" && name != "-" {
		return name
	}
	return fallback
}

func (c *Client) GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}
	return cli.GetGroupInfo(ctx, jid)
}

// Account returns the JID and push name of the linked account.
func (c *Client) Account() (types.JID, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil || c.client.Store == nil || c.client.Store.ID == nil {
		return types.JID{}, ""
	}
	return c.client.Store.ID.ToNonAD(), c.client.Store.PushName
}

// SendReaction reacts to message id of sender in chat. An empty emoji
// removes the reaction.
func (c *Client) SendReaction(ctx context.Context, chat, sender types.JID, id types.MessageID, emoji string) (types.MessageID, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	resp, err := cli.SendMessage(ctx, chat, cli.BuildReaction(chat, sender, id, emoji))
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (c *Client) Logout(ctx context.Context) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil {
		return fmt.Errorf("not initialized")
	}
	return cli.Logout(ctx)
}
