package sdk

import (
	"encoding/json"
	"time"
)

// Protocol is the protocol name in initialize.
const Protocol = "trebi-connector/1"

// Methods of trebi-connector/1. The daemon sends requests; the adapter sends
// the notifications event, status, auth/step, and auth/done.
const (
	MethodInitialize  = "initialize"
	MethodInitialized = "initialized"
	MethodPing        = "ping"
	MethodShutdown    = "shutdown"

	MethodEvent  = "event"
	MethodStatus = "status"

	MethodAuthStatus = "auth/status"
	MethodAuthBegin  = "auth/begin"
	MethodAuthSubmit = "auth/submit"
	MethodAuthCancel = "auth/cancel"
	MethodAuthLogout = "auth/logout"
	MethodAuthStep   = "auth/step"
	MethodAuthDone   = "auth/done"

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

// Channel features. The SDK derives the method features from the optional
// interfaces of an adapter. The attachment features have no method, so an
// adapter declares them in its InitializeResult.
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
)

// MaxLine is the largest line on the wire (1 MiB).
const MaxLine = 1 << 20

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

// InitializeResult is what the adapter delivers. The SDK fills Protocol,
// sets Features from the adapter interfaces, and clears Login when the
// adapter is not an Authenticator.
type InitializeResult struct {
	Protocol string      `json:"protocol"`
	Adapter  AdapterInfo `json:"adapter"`
	Account  *Account    `json:"account,omitempty"`
	Events   []EventDecl `json:"events"`
	Features []string    `json:"features"`
	Limits   Limits      `json:"limits"`
	Login    []string    `json:"login"`
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
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	TS          string          `json:"ts"`
	Room        *Room           `json:"room"`
	Thread      *Thread         `json:"thread"`
	Sender      *Author         `json:"sender"`
	Text        string          `json:"text,omitempty"`
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
}

// AuthState is the session state an Authenticator reports. Reason goes
// into the status notification, not into the auth/status result.
type AuthState struct {
	State   string
	Reason  string
	Account *Account
}

// AuthStatusResult answers auth/status.
type AuthStatusResult struct {
	State   string   `json:"state"`
	Account *Account `json:"account,omitempty"`
}

// StepField is one field of an input step.
type StepField struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Secret bool   `json:"secret,omitempty"`
	Help   string `json:"help,omitempty"`
}

// Step is one login step. The fields depend on the kind.
type Step struct {
	Kind      string      `json:"kind"`
	Data      string      `json:"data,omitempty"`
	URL       string      `json:"url,omitempty"`
	Code      string      `json:"code,omitempty"`
	Fields    []StepField `json:"fields,omitempty"`
	Message   string      `json:"message,omitempty"`
	ExpiresAt string      `json:"expires_at,omitempty"`
}

// AuthBeginParams starts a login flow. An empty kind lets the adapter pick.
type AuthBeginParams struct {
	Kind string `json:"kind,omitempty"`
}

// AuthBeginResult is the first step of the flow.
type AuthBeginResult struct {
	FlowID string `json:"flow_id"`
	Step   Step   `json:"step"`
}

// AuthSubmitParams answers an input step.
type AuthSubmitParams struct {
	FlowID string            `json:"flow_id"`
	Fields map[string]string `json:"fields"`
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
	}
	return ""
}

// Features lists every channel feature in the fixed order.
func Features() []string {
	return []string{
		FeatureRoomsList, FeatureRoomsOpen, FeatureThreads, FeatureThreadsCreate, FeatureHistory,
		FeatureReplay, FeatureTyping, FeatureSeen, FeatureReactions, FeatureEdit,
		FeatureAttachmentsIn, FeatureAttachmentsOut,
	}
}
