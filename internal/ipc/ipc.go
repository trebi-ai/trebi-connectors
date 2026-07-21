// Package ipc provides a small line-delimited JSON RPC over a Unix domain
// socket, used so that `whatsapp-cli send` can forward a message to an already-running
// `whatsapp-cli listen` daemon (which holds the single live WhatsApp connection)
// instead of opening a second, conflicting connection.
package ipc

import (
	"path/filepath"
)

// SocketName is the socket file created inside the store directory.
const SocketName = "whatsapp-cli.sock"

// maxSocketPath is the conservative upper bound on a Unix socket path length.
// macOS caps sun_path at 104 bytes; Linux at 108. Use the smaller so the same
// store dir works on both.
const maxSocketPath = 104

// SocketPath returns the socket path for a given store directory.
func SocketPath(storeDir string) string {
	return filepath.Join(storeDir, SocketName)
}

// Request is a command sent by a client to the daemon over the socket.
type Request struct {
	// Cmd is "send_text", "send_file", "send_chat_presence", "media_download", or "subscribe".
	Cmd string `json:"cmd"`
	To  string `json:"to,omitempty"`

	// send_text
	Message string `json:"message,omitempty"`

	// send_file
	Path     string `json:"path,omitempty"`
	Filename string `json:"filename,omitempty"`
	Caption  string `json:"caption,omitempty"`
	Mime     string `json:"mime,omitempty"`

	// send_chat_presence
	State string `json:"state,omitempty"` // composing | paused
	Media string `json:"media,omitempty"` // text | audio

	// media_download
	Chat   string `json:"chat,omitempty"`
	MsgID  string `json:"msg_id,omitempty"`
	Output string `json:"output,omitempty"`

	// subscribe: the secondary listener's own filter. The anchor applies it to
	// the shared WhatsApp event stream and streams matching JSONL lines back on
	// this same connection until it closes.
	Filter *Filter `json:"filter,omitempty"`
}

// Filter describes a single listener's event selection. It mirrors the
// per-listener flags of `whatsapp-cli listen`.
type Filter struct {
	// Categories is the set of enabled event categories ("all" already expanded).
	Categories  []string `json:"categories"`
	ChatFilter  string   `json:"chat,omitempty"`
	FromFilter  string   `json:"from,omitempty"`
	ExcludeSelf bool     `json:"exclude_self,omitempty"`
	Raw         bool     `json:"raw,omitempty"`
}

// Response is the daemon's reply to a Request.
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`

	// Populated on success.
	ID   string            `json:"id,omitempty"`
	To   string            `json:"to,omitempty"`
	File map[string]string `json:"file,omitempty"`

	// send_chat_presence
	State string `json:"state,omitempty"`
	Media string `json:"media,omitempty"`

	// media_download
	Path      string `json:"path,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	MimeType  string `json:"mime_type,omitempty"`
	Chat      string `json:"chat,omitempty"`
}
