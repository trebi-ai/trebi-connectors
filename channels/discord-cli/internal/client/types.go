package client

import "time"

// User represents a Discord user.
type User struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	Discriminator string `json:"discriminator"`
	GlobalName    string `json:"global_name,omitempty"`
	Avatar        string `json:"avatar,omitempty"`
	Bot           bool   `json:"bot,omitempty"`
	Email         string `json:"email,omitempty"`
}

// Guild represents a Discord server.
type Guild struct {
	ID                       string `json:"id"`
	Name                     string `json:"name"`
	Icon                     string `json:"icon,omitempty"`
	Owner                    bool   `json:"owner,omitempty"`
	OwnerID                  string `json:"owner_id,omitempty"`
	Permissions              string `json:"permissions,omitempty"`
	MemberCount              int    `json:"member_count,omitempty"`
	ApproximateMemberCount   int    `json:"approximate_member_count,omitempty"`
	ApproximatePresenceCount int    `json:"approximate_presence_count,omitempty"`
}

// Channel represents a Discord channel.
type Channel struct {
	ID                 string          `json:"id"`
	Type               int             `json:"type"`
	GuildID            string          `json:"guild_id,omitempty"`
	Name               string          `json:"name,omitempty"`
	Topic              string          `json:"topic,omitempty"`
	Position           int             `json:"position,omitempty"`
	ParentID           string          `json:"parent_id,omitempty"`
	NSFW               bool            `json:"nsfw,omitempty"`
	RateLimitPerUser   int             `json:"rate_limit_per_user,omitempty"`
	LastMessageID      string          `json:"last_message_id,omitempty"`
	ThreadMetadata     *ThreadMetadata `json:"thread_metadata,omitempty"`
	Recipients         []User          `json:"recipients,omitempty"`
	MessageCount       int             `json:"message_count,omitempty"`
	MemberCount        int             `json:"member_count,omitempty"`
	OwnerID            string          `json:"owner_id,omitempty"`
	TotalMessagesSent  int             `json:"total_message_sent,omitempty"`
	DefaultAutoArchive int             `json:"default_auto_archive_duration,omitempty"`
}

// ChannelTypeName returns a human-readable name for a channel type int.
func ChannelTypeName(t int) string {
	switch t {
	case 0:
		return "text"
	case 2:
		return "voice"
	case 4:
		return "category"
	case 5:
		return "announcement"
	case 10:
		return "announcement_thread"
	case 11:
		return "public_thread"
	case 12:
		return "private_thread"
	case 13:
		return "stage"
	case 15:
		return "forum"
	default:
		return "unknown"
	}
}

// ThreadMetadata holds thread-specific fields.
type ThreadMetadata struct {
	Archived            bool      `json:"archived"`
	AutoArchiveDuration int       `json:"auto_archive_duration"`
	ArchiveTimestamp    time.Time `json:"archive_timestamp"`
	Locked              bool      `json:"locked"`
}

// Message represents a Discord message.
type Message struct {
	ID               string            `json:"id"`
	ChannelID        string            `json:"channel_id"`
	GuildID          string            `json:"guild_id,omitempty"`
	Author           *User             `json:"author,omitempty"`
	Content          string            `json:"content"`
	Timestamp        time.Time         `json:"timestamp"`
	EditedTimestamp  *time.Time        `json:"edited_timestamp,omitempty"`
	TTS              bool              `json:"tts,omitempty"`
	MentionEveryone  bool              `json:"mention_everyone,omitempty"`
	Attachments      []Attachment      `json:"attachments,omitempty"`
	Embeds           []Embed           `json:"embeds,omitempty"`
	Reactions        []Reaction        `json:"reactions,omitempty"`
	Pinned           bool              `json:"pinned,omitempty"`
	Type             int               `json:"type"`
	MessageReference *MessageReference `json:"message_reference,omitempty"`
	Thread           *Channel          `json:"thread,omitempty"`
}

// Embed represents a Discord rich embed.
type Embed struct {
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description,omitempty"`
	URL         string       `json:"url,omitempty"`
	Color       int          `json:"color,omitempty"`
	Footer      *EmbedFooter `json:"footer,omitempty"`
	Image       *EmbedMedia  `json:"image,omitempty"`
	Thumbnail   *EmbedMedia  `json:"thumbnail,omitempty"`
	Author      *EmbedAuthor `json:"author,omitempty"`
	Fields      []EmbedField `json:"fields,omitempty"`
	Timestamp   string       `json:"timestamp,omitempty"`
}

// EmbedFooter is the footer of an embed.
type EmbedFooter struct {
	Text    string `json:"text"`
	IconURL string `json:"icon_url,omitempty"`
}

// EmbedMedia is an image or thumbnail in an embed.
type EmbedMedia struct {
	URL    string `json:"url"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// EmbedAuthor is the author section of an embed.
type EmbedAuthor struct {
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
	IconURL string `json:"icon_url,omitempty"`
}

// EmbedField is a field inside an embed.
type EmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

// Reaction represents a reaction on a message.
type Reaction struct {
	Count int   `json:"count"`
	Me    bool  `json:"me"`
	Emoji Emoji `json:"emoji"`
}

// Emoji represents a Discord emoji.
type Emoji struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	Animated bool   `json:"animated,omitempty"`
}

// Attachment represents a file attachment on a message.
type Attachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	Size        int    `json:"size"`
	URL         string `json:"url"`
	ProxyURL    string `json:"proxy_url,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
}

// MessageReference identifies a referenced message (for replies).
type MessageReference struct {
	MessageID string `json:"message_id,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	GuildID   string `json:"guild_id,omitempty"`
}

// ActiveThreadsResponse is the response from GET /guilds/{id}/threads/active.
type ActiveThreadsResponse struct {
	Threads []Channel `json:"threads"`
}

// ArchivedThreadsResponse is the response from GET /channels/{id}/threads/archived/public.
type ArchivedThreadsResponse struct {
	Threads []Channel `json:"threads"`
	HasMore bool      `json:"has_more"`
}
