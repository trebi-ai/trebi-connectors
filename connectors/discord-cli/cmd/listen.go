package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/discord-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/discord-cli/internal/gateway"
)

// Discord channel types for threads.
const (
	channelTypeAnnouncementThread = 10
	channelTypePublicThread       = 11
	channelTypePrivateThread      = 12
)

// ListenCommand returns the `listen` command for streaming gateway events.
func ListenCommand() *cli.Command {
	return &cli.Command{
		Name:  "listen",
		Usage: "Listen to real-time Discord events via Gateway",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "server", Aliases: []string{"s"}, Usage: "guild ID to filter"},
			&cli.StringFlag{Name: "channel", Aliases: []string{"c"}, Usage: "channel ID to filter (includes child threads)"},
			&cli.StringFlag{Name: "events", Aliases: []string{"e"}, Usage: "comma-separated event categories: messages,reactions,members,voice,threads (default: all)"},
			&cli.BoolFlag{Name: "include-bots", Usage: "include bot messages"},
		},
		Action: listenAction,
	}
}

// intentBits maps event categories to Discord Gateway intent bits.
// messages includes GUILD_MESSAGES + DIRECT_MESSAGES + MESSAGE_CONTENT so
// listen --events messages receives both guild and DM MESSAGE_* events.
// threads uses GUILDS (always enabled) — THREAD_* events need no extra intent.
var intentBits = map[string]int{
	"guilds":    1 << 0,
	"members":   1 << 1,
	"voice":     1 << 7,
	"messages":  (1 << 9) | (1 << 12) | (1 << 15), // guild + DM + content
	"reactions": (1 << 10) | (1 << 13),            // guild + DM reactions
	"threads":   0,                                // GUILDS already covers THREAD_*
}

// eventCategoryMap maps Discord event types to categories for filtering.
var eventCategoryMap = map[string]string{
	"MESSAGE_CREATE":          "messages",
	"MESSAGE_UPDATE":          "messages",
	"MESSAGE_DELETE":          "messages",
	"MESSAGE_REACTION_ADD":    "reactions",
	"MESSAGE_REACTION_REMOVE": "reactions",
	"GUILD_MEMBER_ADD":        "members",
	"GUILD_MEMBER_REMOVE":     "members",
	"VOICE_STATE_UPDATE":      "voice",
	"THREAD_CREATE":           "threads",
	"THREAD_UPDATE":           "threads",
	"THREAD_DELETE":           "threads",
	"THREAD_LIST_SYNC":        "threads",
	"THREAD_MEMBER_UPDATE":    "threads",
}

func isThreadChannelType(t int) bool {
	return t == channelTypeAnnouncementThread || t == channelTypePublicThread || t == channelTypePrivateThread
}

// threadParentCache maps thread IDs → parent channel IDs from gateway events
// (and optional REST lookups) so --channel can match messages in child threads.
type threadParentCache struct {
	mu             sync.RWMutex
	parentByThread map[string]string
	// channel IDs we REST-resolved and found are not threads (avoid repeat GETs)
	notThread map[string]struct{}
	// optional REST client for mid-session unknown channel_id lookups
	rest *client.Client
}

func newThreadParentCache(rest *client.Client) *threadParentCache {
	return &threadParentCache{
		parentByThread: make(map[string]string),
		notThread:      make(map[string]struct{}),
		rest:           rest,
	}
}

func (c *threadParentCache) set(threadID, parentID string) {
	if threadID == "" || parentID == "" {
		return
	}
	c.mu.Lock()
	c.parentByThread[threadID] = parentID
	delete(c.notThread, threadID)
	c.mu.Unlock()
}

func (c *threadParentCache) remove(threadID string) {
	if threadID == "" {
		return
	}
	c.mu.Lock()
	delete(c.parentByThread, threadID)
	c.mu.Unlock()
}

func (c *threadParentCache) parentOf(threadID string) (string, bool) {
	c.mu.RLock()
	p, ok := c.parentByThread[threadID]
	c.mu.RUnlock()
	return p, ok
}

// observe updates the cache from gateway dispatch payloads (always, even when
// the event is not emitted under the selected --events categories).
func (c *threadParentCache) observe(eventType string, data json.RawMessage) {
	switch eventType {
	case "THREAD_CREATE", "THREAD_UPDATE":
		var ch channelLike
		if json.Unmarshal(data, &ch) != nil {
			return
		}
		if ch.ID != "" && ch.ParentID != "" {
			c.set(ch.ID, ch.ParentID)
		}
	case "THREAD_DELETE":
		var ch channelLike
		if json.Unmarshal(data, &ch) != nil {
			return
		}
		c.remove(ch.ID)
	case "THREAD_LIST_SYNC":
		var sync threadListSync
		if json.Unmarshal(data, &sync) != nil {
			return
		}
		for _, th := range sync.Threads {
			if th.ID != "" && th.ParentID != "" {
				c.set(th.ID, th.ParentID)
			}
		}
	case "CHANNEL_CREATE", "CHANNEL_UPDATE":
		var ch channelLike
		if json.Unmarshal(data, &ch) != nil {
			return
		}
		if isThreadChannelType(ch.Type) && ch.ID != "" && ch.ParentID != "" {
			c.set(ch.ID, ch.ParentID)
		}
	case "CHANNEL_DELETE":
		var ch channelLike
		if json.Unmarshal(data, &ch) != nil {
			return
		}
		if isThreadChannelType(ch.Type) {
			c.remove(ch.ID)
		}
	}
}

// resolveParent returns the parent channel id when channelID is a thread.
// Uses the gateway cache first; one REST GET /channels/{id} if still unknown and rest is set.
func (c *threadParentCache) resolveParent(channelID string) (string, bool) {
	if channelID == "" || c == nil {
		return "", false
	}
	if p, ok := c.parentOf(channelID); ok {
		return p, true
	}

	c.mu.RLock()
	_, knownNot := c.notThread[channelID]
	c.mu.RUnlock()
	if knownNot || c.rest == nil {
		return "", false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var ch client.Channel
	if err := c.rest.DoJSON(ctx, "GET", "/channels/"+channelID, nil, &ch); err != nil {
		return "", false
	}
	if isThreadChannelType(ch.Type) && ch.ParentID != "" {
		c.set(ch.ID, ch.ParentID)
		return ch.ParentID, true
	}
	c.mu.Lock()
	c.notThread[channelID] = struct{}{}
	c.mu.Unlock()
	return "", false
}

// matchesChannel reports whether channelID is the filter or a known child thread.
func (c *threadParentCache) matchesChannel(channelID, filter string) bool {
	if filter == "" {
		return true
	}
	if channelID == "" {
		return false
	}
	if channelID == filter {
		return true
	}
	p, ok := c.resolveParent(channelID)
	return ok && p == filter
}

// enrichParentID adds d.parent_id on MESSAGE_*/REACTION_* when channel_id is a thread.
// Discord does not include parent on message payloads; we inject from the thread cache.
// Returns the (possibly unchanged) payload.
func enrichParentID(eventType string, data json.RawMessage, cache *threadParentCache) json.RawMessage {
	switch eventType {
	case "MESSAGE_CREATE", "MESSAGE_UPDATE", "MESSAGE_DELETE",
		"MESSAGE_REACTION_ADD", "MESSAGE_REACTION_REMOVE":
	default:
		return data
	}
	if cache == nil {
		return data
	}

	var head struct {
		ChannelID string `json:"channel_id"`
		ParentID  string `json:"parent_id"`
	}
	if json.Unmarshal(data, &head) != nil || head.ChannelID == "" {
		return data
	}
	// Already present (shouldn't happen on messages; leave Discord data alone).
	if head.ParentID != "" {
		return data
	}

	parent, ok := cache.resolveParent(head.ChannelID)
	if !ok || parent == "" {
		return data
	}

	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) != nil {
		return data
	}
	raw, err := json.Marshal(parent)
	if err != nil {
		return data
	}
	m["parent_id"] = raw
	out, err := json.Marshal(m)
	if err != nil {
		return data
	}
	return out
}

// channelLike is a partial Channel for THREAD_* / CHANNEL_* payloads.
type channelLike struct {
	ID       string `json:"id"`
	Type     int    `json:"type"`
	GuildID  string `json:"guild_id"`
	ParentID string `json:"parent_id"`
}

// threadListSync is the THREAD_LIST_SYNC payload.
type threadListSync struct {
	GuildID    string        `json:"guild_id"`
	ChannelIDs []string      `json:"channel_ids"`
	Threads    []channelLike `json:"threads"`
}

// threadMemberUpdate is the THREAD_MEMBER_UPDATE payload.
type threadMemberUpdate struct {
	ID      string `json:"id"` // thread id
	GuildID string `json:"guild_id"`
	UserID  string `json:"user_id"`
}

// eventPartial is the common slice of fields used for filtering.
type eventPartial struct {
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
	Author    *struct {
		Bot bool `json:"bot"`
	} `json:"author"`
}

// categoryAllowed reports whether eventType is in the selected categories.
// allEvents true means every type is allowed (including unmapped gateway events).
func categoryAllowed(eventType string, allEvents bool, categories []string) bool {
	if allEvents {
		return true
	}
	cat, known := eventCategoryMap[eventType]
	if !known {
		return false
	}
	for _, c := range categories {
		if c == cat {
			return true
		}
	}
	return false
}

// eventPassesFilters applies server, channel (incl. child threads), and bot filters.
// cache may be nil when channelFilter is empty.
func eventPassesFilters(eventType string, data json.RawMessage, serverFilter, channelFilter string, includeBots bool, cache *threadParentCache) bool {
	var partial eventPartial
	_ = json.Unmarshal(data, &partial)

	// Server filter: require guild_id match when set (drops DMs).
	guildID := partial.GuildID
	if guildID == "" {
		// THREAD_* / channel payloads often put guild_id on the channel object only;
		// MESSAGE_* already set partial.GuildID. Re-read for thread shapes below.
		switch eventType {
		case "THREAD_CREATE", "THREAD_UPDATE", "THREAD_DELETE", "CHANNEL_CREATE", "CHANNEL_UPDATE", "CHANNEL_DELETE":
			var ch channelLike
			if json.Unmarshal(data, &ch) == nil {
				guildID = ch.GuildID
			}
		case "THREAD_LIST_SYNC":
			var sync threadListSync
			if json.Unmarshal(data, &sync) == nil {
				guildID = sync.GuildID
			}
		case "THREAD_MEMBER_UPDATE":
			var tm threadMemberUpdate
			if json.Unmarshal(data, &tm) == nil {
				guildID = tm.GuildID
			}
		}
	}
	if serverFilter != "" && guildID != serverFilter {
		return false
	}

	if channelFilter != "" {
		if !channelFilterMatch(eventType, data, partial.ChannelID, channelFilter, cache) {
			return false
		}
	}

	// Bot-author filter only when author is present (messages).
	if !includeBots && partial.Author != nil && partial.Author.Bot {
		return false
	}

	return true
}

// channelFilterMatch applies --channel against the event shape.
func channelFilterMatch(eventType string, data json.RawMessage, channelID, filter string, cache *threadParentCache) bool {
	if filter == "" {
		return true
	}
	if cache == nil {
		return channelID == filter
	}

	switch eventType {
	case "THREAD_CREATE", "THREAD_UPDATE", "THREAD_DELETE":
		var ch channelLike
		if json.Unmarshal(data, &ch) != nil {
			return false
		}
		return ch.ID == filter || ch.ParentID == filter

	case "THREAD_LIST_SYNC":
		var sync threadListSync
		if json.Unmarshal(data, &sync) != nil {
			return false
		}
		for _, id := range sync.ChannelIDs {
			if id == filter {
				return true
			}
		}
		for _, th := range sync.Threads {
			if th.ID == filter || th.ParentID == filter {
				return true
			}
		}
		return false

	case "THREAD_MEMBER_UPDATE":
		var tm threadMemberUpdate
		if json.Unmarshal(data, &tm) != nil {
			return false
		}
		if tm.ID == filter {
			return true
		}
		return cache.matchesChannel(tm.ID, filter)

	default:
		// MESSAGE_*, REACTION_*, VOICE_*, etc.: channel_id is parent or thread id.
		return cache.matchesChannel(channelID, filter)
	}
}

func listenAction(c *cli.Context) error {
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}

	eventsFlag := c.String("events")
	allEvents := eventsFlag == "" || eventsFlag == "all"

	var categories []string
	if !allEvents {
		categories = strings.Split(eventsFlag, ",")
		for i := range categories {
			categories[i] = strings.TrimSpace(categories[i])
		}
	}

	intents := intentBits["guilds"]
	if allEvents {
		for _, bits := range intentBits {
			intents |= bits
		}
	} else {
		for _, cat := range categories {
			if bits, ok := intentBits[cat]; ok {
				intents |= bits
			}
		}
	}

	serverFilter := c.String("server")
	channelFilter := c.String("channel")
	includeBots := c.Bool("include-bots")

	// REST for optional parent resolution (filter + parent_id enrichment).
	cache := newThreadParentCache(cl)

	gw := gateway.New(cl.Token, intents)
	ready, err := gw.Connect()
	if err != nil {
		return err
	}
	defer gw.Close()

	fmt.Fprintf(os.Stderr, "Connected as %s, listening for events...\n", ready.User.Username)

	return gw.Listen(func(eventType string, data json.RawMessage) {
		// Keep parent map warm from gateway even if category is not selected.
		cache.observe(eventType, data)

		if !categoryAllowed(eventType, allEvents, categories) {
			return
		}
		if !eventPassesFilters(eventType, data, serverFilter, channelFilter, includeBots, cache) {
			return
		}

		out := map[string]any{
			"t": eventType,
			"d": enrichParentID(eventType, data, cache),
		}
		line, _ := json.Marshal(out)
		fmt.Fprintln(os.Stdout, string(line))
	})
}
