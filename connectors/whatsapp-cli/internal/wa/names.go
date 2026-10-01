package wa

import (
	"context"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
)

const (
	groupNameTTL = 6 * time.Hour
	// groupFailTTL keeps a failed lookup, so that a burst of messages does
	// not repeat it.
	groupFailTTL = time.Minute
)

// groupSource gets the group info from WhatsApp.
type groupSource interface {
	IsConnected() bool
	GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error)
}

// groupNames caches the subjects of groups.
type groupNames struct {
	mu      sync.Mutex
	entries map[types.JID]groupName
	now     func() time.Time
}

type groupName struct {
	name    string // "" after a failed lookup
	expires time.Time
}

func newGroupNames() *groupNames {
	return &groupNames{entries: map[types.JID]groupName{}, now: time.Now}
}

// cached returns the name of jid and true when the cache has a live entry.
// The name of a failed lookup is "".
func (g *groupNames) cached(jid types.JID) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.entries[jid]
	if !ok || g.now().After(e.expires) {
		return "", false
	}
	return e.name, true
}

func (g *groupNames) remember(jid types.JID, name string) {
	name = strings.TrimSpace(name)
	ttl := groupNameTTL
	if name == "" {
		ttl = groupFailTTL
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.entries[jid] = groupName{name: name, expires: g.now().Add(ttl)}
}

// resolve returns the name of jid from the cache, or from src when src is
// connected. It returns "" when the name is not known.
func (g *groupNames) resolve(ctx context.Context, src groupSource, jid types.JID) string {
	if name, ok := g.cached(jid); ok {
		return name
	}
	if !src.IsConnected() {
		return ""
	}
	name := ""
	if info, err := src.GetGroupInfo(ctx, jid); err == nil && info != nil {
		name = info.GroupName.Name
	} else if ctx.Err() != nil {
		return ""
	}
	g.remember(jid, name)
	return strings.TrimSpace(name)
}
