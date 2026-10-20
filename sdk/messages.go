package sdk

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"strings"
	"time"
)

// Protocol is the protocol name in initialize.
const Protocol = "trebi-connector/1"

// Methods of trebi-connector/1. The daemon sends requests; the adapter sends
// the notifications event, status, auth/step, auth/done, and
// subscriptions/changed.
const (
	MethodInitialize  = "initialize"
	MethodInitialized = "initialized"
	MethodPing        = "ping"
	MethodShutdown    = "shutdown"

	MethodEvent  = "event"
	MethodStatus = "status"

	MethodAuthStatus  = "auth/status"
	MethodAuthMethods = "auth/methods"
	MethodAuthBegin   = "auth/begin"
	MethodAuthSubmit  = "auth/submit"
	MethodAuthCancel  = "auth/cancel"
	MethodAuthLogout  = "auth/logout"
	MethodAuthStep    = "auth/step"
	MethodAuthDone    = "auth/done"

	MethodMessagesSend    = "messages/send"
	MethodMessagesEdit    = "messages/edit"
	MethodMessagesHistory = "messages/history"
	MethodMessagesSeen    = "messages/seen"
	MethodEventsReplay    = "events/replay"
	MethodRoomsList       = "rooms/list"
	MethodRoomsGet        = "rooms/get"
	MethodRoomsOpen       = "rooms/open"
	MethodThreadsList     = "threads/list"
	MethodThreadsCreate   = "threads/create"
	MethodTyping          = "typing"
	MethodReactionsAdd    = "reactions/add"

	MethodSubscriptionsOptions = "subscriptions/options"
	MethodSubscriptionsSync    = "subscriptions/sync"
	MethodSubscriptionsSubmit  = "subscriptions/submit"
	MethodWebhookReceive       = "webhook/receive"
	// MethodSubscriptionsChanged asks the daemon for a new sync.
	MethodSubscriptionsChanged = "subscriptions/changed"
)

// Subscription states.
const (
	SubscriptionPending        = "pending"
	SubscriptionActive         = "active"
	SubscriptionPolling        = "polling"
	SubscriptionActionRequired = "action_required"
	SubscriptionError          = "error"
)

// Subscription modes: how the platform delivers the events of one
// subscription.
const (
	ModeAPI    = "api"
	ModeManual = "manual"
	ModeStream = "stream"
	ModePoll   = "poll"
)

// Adapter states of the status notification.
const (
	StateConnecting   = "connecting"
	StateConnected    = "connected"
	StateAuthRequired = "auth_required"
	StateAuthPending  = "auth_pending"
	StateRateLimited  = "rate_limited"
	StateError        = "error"
)

// Reasons of auth_required.
const (
	ReasonNone      = "none"
	ReasonExpired   = "expired"
	ReasonRevoked   = "revoked"
	ReasonLoggedOut = "logged_out"
	// ReasonMissingInput is a required input that is not set (Trebi mode).
	ReasonMissingInput = "missing_input"
)

// Login step kinds.
const (
	StepQR         = "qr"
	StepDeviceCode = "device_code"
	StepURL        = "url"
	StepInput      = "input"
	StepWait       = "wait"
)

// Login method types. Trebi fixes the set; the UI shows the methods in
// this order.
const (
	LoginExisting = "existing" // a login that is on the computer of Trebi
	LoginBrowser  = "browser"  // a web page or a device code
	LoginPhone    = "phone"    // a code that the user scans or types on a phone
	LoginKey      = "key"      // a key or token that the user pastes
)

// LoginTypes lists the login method types in the order of the UI.
func LoginTypes() []string { return []string{LoginExisting, LoginBrowser, LoginPhone, LoginKey} }

// Room kinds.
const (
	RoomDM      = "dm"
	RoomGroup   = "group"
	RoomChannel = "channel"
)

// Message formats.
const (
	FormatText     = "text"
	FormatMarkdown = "markdown"
	FormatHTML     = "html"
)

// Features. The SDK derives the method features from the optional
// interfaces of an adapter. The attachment features and replies have no
// method, so an adapter declares them in its InitializeResult.
const (
	FeatureRoomsList      = "rooms.list"
	FeatureRoomsOpen      = "rooms.open"
	FeatureThreads        = "threads"
	FeatureThreadsCreate  = "threads.create"
	FeatureHistory        = "history"
	FeatureReplay         = "replay"
	FeatureTyping         = "typing"
	FeatureSeen           = "seen"
	FeatureReactions      = "reactions"
	FeatureEdit           = "edit"
	FeatureAttachmentsIn  = "attachments.in"
	FeatureAttachmentsOut = "attachments.out"
	FeatureReplies        = "replies"
	FeatureSubscriptions  = "subscriptions"
	FeatureWebhooks       = "webhooks"
)

// MaxLine is the largest line on the wire (4 MiB).
const MaxLine = 4 << 20

// TimeLayout is the event time format: UTC with microseconds.
const TimeLayout = "2006-01-02T15:04:05.000000Z"

// Empty is the params or result of a message with no fields.
type Empty struct{}

// DaemonInfo names the daemon build.
type DaemonInfo struct {
	Version string `json:"version"`
}

// InstanceInfo names the instance the adapter serves.
type InstanceInfo struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Workspace string `json:"workspace,omitempty"`
}

// InitializeParams starts the session. Cursor is the id of the last event
// the daemon stored, when there is one.
type InitializeParams struct {
	Protocol string       `json:"protocol"`
	Daemon   DaemonInfo   `json:"daemon"`
	Instance InstanceInfo `json:"instance"`
	Cursor   string       `json:"cursor,omitempty"`
	// Webhook is the hosted hook of the connection, or nil when the user
	// has no Trebi Cloud link.
	Webhook *Webhook `json:"webhook,omitempty"`
}

// Webhook is the hosted URL of a connection. Secret is a suggestion for a
// platform that accepts one.
type Webhook struct {
	URL    string `json:"url"`
	Secret string `json:"secret"`
}

// AdapterInfo names the adapter build.
type AdapterInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Account is the account the adapter is logged in to.
type Account struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// EventDecl is one event type the adapter emits.
type EventDecl struct {
	Type string `json:"type"`
}

// Limits bound one message.
type Limits struct {
	MaxText int      `json:"max_text,omitempty"`
	Formats []string `json:"formats,omitempty"`
}

// InitializeResult is what the adapter delivers. The SDK fills Protocol
// and sets Features from the adapter interfaces.
type InitializeResult struct {
	Protocol string      `json:"protocol"`
	Adapter  AdapterInfo `json:"adapter"`
	Account  *Account    `json:"account,omitempty"`
	Events   []EventDecl `json:"events"`
	Features []string    `json:"features"`
	Limits   Limits      `json:"limits"`
}

// Room is one conversation place: a channel, a group, or a DM.
type Room struct {
	ID             string `json:"id"`
	Name           string `json:"name,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Parent         string `json:"parent,omitempty"`
	LastActivityAt string `json:"last_activity_at,omitempty"`
}

// Thread is one conversation line in a room.
type Thread struct {
	ID             string   `json:"id"`
	Title          string   `json:"title,omitempty"`
	LastActivityAt string   `json:"last_activity_at,omitempty"`
	Participants   []string `json:"participants,omitempty"`
}

// Author is who wrote an event. ID is the identity for trigger allowlists.
type Author struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Self bool   `json:"self,omitempty"`
	Bot  bool   `json:"bot,omitempty"`
}

// Attachment is one file on a message.
type Attachment struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Mime string `json:"mime,omitempty"`
	Size int64  `json:"size,omitempty"`
	URL  string `json:"url,omitempty"`
	Path string `json:"path,omitempty"`
}

// Event is one inbound event. ID is the dedupe key and the daemon cursor.
type Event struct {
	ID     string  `json:"id"`
	Type   string  `json:"type"`
	TS     string  `json:"ts"`
	Room   *Room   `json:"room"`
	Thread *Thread `json:"thread"`
	Sender *Author `json:"sender"`
	// Text is what a person sees: the body, then the readable parts of
	// rich content, one part per line. Never raw JSON.
	Text string `json:"text,omitempty"`
	// ReplyTo is the id of the message that this message quotes or answers.
	ReplyTo     string          `json:"reply_to,omitempty"`
	Attachments []Attachment    `json:"attachments,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	Raw         json.RawMessage `json:"raw,omitempty"`
}

// Status is the status notification.
type Status struct {
	State   string   `json:"state"`
	Reason  string   `json:"reason,omitempty"`
	Message string   `json:"message,omitempty"`
	Account *Account `json:"account,omitempty"`
	// Accounts has one row per account of a connection that follows
	// several accounts. One account can need a login while the others run.
	Accounts []AccountState `json:"accounts,omitempty"`
	// Links are pages of the adapter that the connector panel shows. A
	// status with no links keeps the last links; an empty list clears them.
	Links []Link `json:"links,omitempty"`
}

// Link is one page that the adapter serves, for example its own UI. Trebi
// keeps only links to 127.0.0.1, localhost, or the tailnet.
type Link struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// AuthState is the session state an Authenticator reports. Reason goes
// into the status notification, not into the auth/status result. Accounts
// has one row per account of a connection that follows several accounts.
type AuthState struct {
	State    string
	Reason   string
	Account  *Account
	Accounts []AccountState
}

// AuthStatusResult answers auth/status.
type AuthStatusResult struct {
	State    string         `json:"state"`
	Account  *Account       `json:"account,omitempty"`
	Accounts []AccountState `json:"accounts,omitempty"`
}

// AccountState is the state of one account of a connection.
type AccountState struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

// LoginMethod is one way to log in that the adapter offers. An unavailable
// method has a reason that the user can read.
type LoginMethod struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Label     string `json:"label,omitempty"`
	Help      string `json:"help,omitempty"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// AuthMethodsResult answers auth/methods.
type AuthMethodsResult struct {
	Methods []LoginMethod `json:"methods"`
}

// StepField is one field of an input step, or the pasted fallback of a url
// step. URL is a page where the user gets the value. A field with choices
// is a pick list; with Multiple, the user can pick more than one.
type StepField struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Secret   bool     `json:"secret,omitempty"`
	Help     string   `json:"help,omitempty"`
	URL      string   `json:"url,omitempty"`
	Choices  []Choice `json:"choices,omitempty"`
	Multiple bool     `json:"multiple,omitempty"`
	Default  Value    `json:"default,omitempty"`
}

// Choice is one item of a pick list.
type Choice struct {
	Value       string `json:"value"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
}

// Value is the answer of one field: a string, or a list of strings for a
// field with Multiple. It reads both forms and writes one string when it
// holds one item.
type Value []string

// UnmarshalJSON reads a string or a list of strings.
func (v *Value) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*v = Value{one}
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		return errors.New("must be a string or a list of strings")
	}
	*v = list
	return nil
}

// MarshalJSON writes one item as a string.
func (v Value) MarshalJSON() ([]byte, error) {
	if len(v) == 1 {
		return json.Marshal(v[0])
	}
	return json.Marshal([]string(v))
}

// String returns the first item, or "".
func (v Value) String() string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// Step is one login step. The fields depend on the kind: an input step has
// fields, and a url step can have fields for a pasted fallback.
type Step struct {
	Kind      string      `json:"kind"`
	Data      string      `json:"data,omitempty"`
	URL       string      `json:"url,omitempty"`
	Code      string      `json:"code,omitempty"`
	Fields    []StepField `json:"fields,omitempty"`
	Message   string      `json:"message,omitempty"`
	ExpiresAt string      `json:"expires_at,omitempty"`
}

// AuthBeginParams starts a login flow with one method of auth/methods. An
// empty method means the first available one.
type AuthBeginParams struct {
	Method string `json:"method,omitempty"`
}

// AuthBeginResult is the first step of the flow.
type AuthBeginResult struct {
	FlowID string `json:"flow_id"`
	Step   Step   `json:"step"`
}

// AuthSubmitParams answers an input step, or the fields of a url step.
type AuthSubmitParams struct {
	FlowID string           `json:"flow_id"`
	Fields map[string]Value `json:"fields"`
}

// AuthSubmitResult carries the next step, when the adapter knows it now.
type AuthSubmitResult struct {
	Step *Step `json:"step,omitempty"`
}

// AuthFlowParams names a flow (auth/cancel).
type AuthFlowParams struct {
	FlowID string `json:"flow_id"`
}

// AuthStepParams is the auth/step notification.
type AuthStepParams struct {
	FlowID string `json:"flow_id"`
	Step   Step   `json:"step"`
}

// AuthDoneParams is the auth/done notification.
type AuthDoneParams struct {
	FlowID  string   `json:"flow_id"`
	OK      bool     `json:"ok"`
	Account *Account `json:"account,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// SendParams sends one message. Key is the outbox row id of the daemon.
type SendParams struct {
	Room        string       `json:"room"`
	Thread      string       `json:"thread,omitempty"`
	Text        string       `json:"text"`
	Format      string       `json:"format"`
	ReplyTo     string       `json:"reply_to,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Key         string       `json:"key"`
}

// SendResult names the sent message.
type SendResult struct {
	MessageID string `json:"message_id"`
	Thread    string `json:"thread,omitempty"`
}

// EditParams edits a sent message.
type EditParams struct {
	Room      string `json:"room"`
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}

// HistoryQuery reads older messages of a room.
type HistoryQuery struct {
	Room   string `json:"room"`
	Thread string `json:"thread,omitempty"`
	Before string `json:"before,omitempty"`
	Limit  int    `json:"limit"`
}

// EventPage is a page of events.
type EventPage struct {
	Events []Event `json:"events"`
	Next   string  `json:"next,omitempty"`
}

// ReplayParams asks for the events after a cursor.
type ReplayParams struct {
	After string `json:"after"`
	Limit int    `json:"limit"`
}

// ReplayResult is the replay. Complete false means the cursor is too old.
type ReplayResult struct {
	Events   []Event `json:"events"`
	Complete bool    `json:"complete"`
}

// RoomQuery searches rooms. Cursor is the opaque page token.
type RoomQuery struct {
	Query  string `json:"query,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit"`
}

// RoomPage is a page of rooms.
type RoomPage struct {
	Rooms []Room `json:"rooms"`
	Next  string `json:"next,omitempty"`
}

// RoomParams names a room (rooms/get).
type RoomParams struct {
	Room string `json:"room"`
}

// RoomOpenParams opens the DM room with a user.
type RoomOpenParams struct {
	User string `json:"user"`
}

// RoomResult carries one room.
type RoomResult struct {
	Room Room `json:"room"`
}

// ThreadQuery lists the threads of a room.
type ThreadQuery struct {
	Room   string `json:"room"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit"`
}

// ThreadPage is a page of threads.
type ThreadPage struct {
	Threads []Thread `json:"threads"`
	Next    string   `json:"next,omitempty"`
}

// CreateThreadParams starts a thread.
type CreateThreadParams struct {
	Room        string `json:"room"`
	Title       string `json:"title,omitempty"`
	FromMessage string `json:"from_message,omitempty"`
}

// ThreadResult carries one thread.
type ThreadResult struct {
	Thread Thread `json:"thread"`
}

// TypingParams shows the typing indicator.
type TypingParams struct {
	Room   string `json:"room"`
	Thread string `json:"thread,omitempty"`
}

// SeenParams marks a message as seen.
type SeenParams struct {
	Room      string `json:"room"`
	MessageID string `json:"message_id"`
}

// ReactionParams adds a reaction.
type ReactionParams struct {
	Room      string `json:"room"`
	MessageID string `json:"message_id"`
	Emoji     string `json:"emoji"`
}

// OptionQuery asks for the options of one dynamic field. Values is the
// current form, because one field can depend on another.
type OptionQuery struct {
	Field  string              `json:"field"`
	Query  string              `json:"q,omitempty"`
	Values map[string][]string `json:"values,omitempty"`
	Cursor string              `json:"cursor,omitempty"`
	Limit  int                 `json:"limit"`
}

// FieldOption is one choice of a dynamic field.
type FieldOption struct {
	Value       string `json:"value"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
}

// OptionPage is a page of options.
type OptionPage struct {
	Options []FieldOption `json:"options"`
	Next    string        `json:"next,omitempty"`
}

// Subscription is one thing that the user watches. Values holds the form
// values by field name. Types is empty for every event type.
type Subscription struct {
	ID     string              `json:"id"`
	Values map[string][]string `json:"values"`
	Types  []string            `json:"types,omitempty"`
}

// Value returns the first value of a field, or "".
func (s Subscription) Value(field string) string {
	if v := s.Values[field]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// SyncParams is the full list that the platform must match. A subscription
// that is not in the list is gone.
type SyncParams struct {
	Subscriptions []Subscription `json:"subscriptions"`
}

// SubscriptionState is how the adapter serves one subscription.
type SubscriptionState struct {
	ID        string  `json:"id"`
	Title     string  `json:"title,omitempty"`
	Room      *Room   `json:"room,omitempty"`
	Mode      string  `json:"mode"`
	State     string  `json:"state"`
	Message   string  `json:"message,omitempty"`
	ExpiresAt string  `json:"expires_at,omitempty"`
	Action    *Action `json:"action,omitempty"`
}

// Action is what a person must do to finish a subscription. Write Text in
// simple words for a person who does not know webhooks.
type Action struct {
	Text string      `json:"text"`
	URL  string      `json:"url,omitempty"`
	Show []ShowValue `json:"show,omitempty"`
	Ask  []AskField  `json:"ask,omitempty"`
}

// ShowValue is one value that the person copies.
type ShowValue struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
}

// AskField is one field that the person fills.
type AskField struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Help     string `json:"help,omitempty"`
	Secret   bool   `json:"secret,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// SyncResult has one state for each subscription of the params, in order.
type SyncResult struct {
	Subscriptions []SubscriptionState `json:"subscriptions"`
}

// SubmitParams carries the answers of a person for one subscription.
type SubmitParams struct {
	ID     string            `json:"id"`
	Fields map[string]string `json:"fields"`
}

// WebhookRequest is one request to the hosted hook of the connection.
// Header keys are lowercase. Handshake marks a request that the cloud
// already answered.
type WebhookRequest struct {
	ID          string            `json:"id"`
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	Query       string            `json:"query,omitempty"`
	ContentType string            `json:"content_type,omitempty"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body"`
	ReceivedAt  string            `json:"received_at"`
	Handshake   bool              `json:"handshake,omitempty"`
}

// Encoding is how a platform writes a signature.
type Encoding int

// Signature encodings.
const (
	EncodingHex Encoding = iota
	EncodingBase64
)

// Header returns one header. The name is not case sensitive.
func (r WebhookRequest) Header(name string) string {
	return r.Headers[strings.ToLower(name)]
}

// VerifyHMAC checks a header of the form "<prefix><HMAC of the body>", for
// example "sha256=<hex>". A platform with another form checks in the
// adapter.
func (r WebhookRequest) VerifyHMAC(secret []byte, header, prefix string, h func() hash.Hash, enc Encoding) bool {
	got, ok := strings.CutPrefix(r.Header(header), prefix)
	if !ok || got == "" || len(secret) == 0 {
		return false
	}
	mac := hmac.New(h, secret)
	mac.Write([]byte(r.Body))
	sum := mac.Sum(nil)
	var want string
	switch enc {
	case EncodingBase64:
		want = base64.StdEncoding.EncodeToString(sum)
	default:
		want = hex.EncodeToString(sum)
		got = strings.ToLower(got)
	}
	return hmac.Equal([]byte(got), []byte(want))
}

// FormatTime writes t in the event time format.
func FormatTime(t time.Time) string { return t.UTC().Format(TimeLayout) }

// FeatureOf returns the channel feature a method needs, or "" for a core
// method.
func FeatureOf(method string) string {
	switch method {
	case MethodMessagesEdit:
		return FeatureEdit
	case MethodMessagesHistory:
		return FeatureHistory
	case MethodEventsReplay:
		return FeatureReplay
	case MethodRoomsList, MethodRoomsGet:
		return FeatureRoomsList
	case MethodRoomsOpen:
		return FeatureRoomsOpen
	case MethodThreadsList:
		return FeatureThreads
	case MethodThreadsCreate:
		return FeatureThreadsCreate
	case MethodTyping:
		return FeatureTyping
	case MethodMessagesSeen:
		return FeatureSeen
	case MethodReactionsAdd:
		return FeatureReactions
	case MethodSubscriptionsOptions, MethodSubscriptionsSync, MethodSubscriptionsSubmit:
		return FeatureSubscriptions
	case MethodWebhookReceive:
		return FeatureWebhooks
	}
	return ""
}

// Features lists every feature in the fixed order.
func Features() []string {
	return []string{
		FeatureRoomsList, FeatureRoomsOpen, FeatureThreads, FeatureThreadsCreate, FeatureHistory,
		FeatureReplay, FeatureTyping, FeatureSeen, FeatureReactions, FeatureEdit,
		FeatureAttachmentsIn, FeatureAttachmentsOut, FeatureReplies, FeatureSubscriptions, FeatureWebhooks,
	}
}
