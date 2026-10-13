package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	channelIDRE = regexp.MustCompile(`^[CDG][A-Z0-9]{7,}$`)
	userIDRE    = regexp.MustCompile(`^[UWB][A-Z0-9]{7,}$`)
	emailRE     = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// IsChannelID reports whether s looks like a conversation id.
func IsChannelID(s string) bool { return channelIDRE.MatchString(s) }

// IsUserID reports whether s looks like a user id.
func IsUserID(s string) bool { return userIDRE.MatchString(s) }

// User is the part of a Slack user that the directory keeps.
type User struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RealName    string `json:"real_name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Email       string `json:"email,omitempty"`
	Bot         bool   `json:"bot,omitempty"`
	Deleted     bool   `json:"deleted,omitempty"`
}

// Label returns the best short name of the user.
func (u User) Label() string {
	for _, s := range []string{u.DisplayName, u.RealName, u.Name} {
		if s != "" {
			return s
		}
	}
	return u.ID
}

// Channel is the part of a conversation that the directory keeps.
type Channel struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	User    string `json:"user,omitempty"` // the other user of a DM
	Private bool   `json:"private,omitempty"`
	IM      bool   `json:"im,omitempty"`
	MPIM    bool   `json:"mpim,omitempty"`
}

// Directory maps channel and user names to ids, with a cache file.
type Directory struct {
	cl   *Client
	path string

	Users      map[string]User    `json:"users"`    // id → user
	Channels   map[string]Channel `json:"channels"` // id → channel
	UsersAt    time.Time          `json:"users_at"` // last full user load
	ChannelsAt time.Time          `json:"channels_at"`
}

// TTL is how long a cached full list stays fresh.
const TTL = 12 * time.Hour

// NewDirectory loads the cache for a workspace (empty when none).
func NewDirectory(cl *Client, cacheDir, workspace string) *Directory {
	d := &Directory{cl: cl, Users: map[string]User{}, Channels: map[string]Channel{}}
	if cacheDir != "" && workspace != "" {
		d.path = filepath.Join(cacheDir, "cache", safeName(workspace)+".json")
		if data, err := os.ReadFile(d.path); err == nil {
			_ = json.Unmarshal(data, d)
			if d.Users == nil {
				d.Users = map[string]User{}
			}
			if d.Channels == nil {
				d.Channels = map[string]Channel{}
			}
		}
	}
	return d
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}

// Save writes the cache file (mode 0600). A directory with no path is a no-op.
func (d *Directory) Save() error {
	if d.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(d.path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return os.WriteFile(d.path, data, 0o600)
}

// ResolveChannel turns a channel reference into an id. It accepts an id, a
// "#name", a bare name, a DM "@user", or an email.
func (d *Directory) ResolveChannel(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("empty channel")
	}
	if IsChannelID(ref) {
		return ref, nil
	}
	if strings.HasPrefix(ref, "@") || emailRE.MatchString(ref) {
		uid, err := d.ResolveUser(ctx, strings.TrimPrefix(ref, "@"))
		if err != nil {
			return "", err
		}
		return d.openIM(ctx, uid)
	}
	name := strings.TrimPrefix(ref, "#")
	if err := d.ensureChannels(ctx); err != nil {
		return "", err
	}
	for _, c := range d.Channels {
		if strings.EqualFold(c.Name, name) {
			return c.ID, nil
		}
	}
	return "", fmt.Errorf("no channel %q (try the id, or `slack-cli conversations find %s`)", ref, name)
}

// ResolveUser turns a user reference into an id. It accepts an id, a
// "@name", a bare name, or an email.
func (d *Directory) ResolveUser(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(strings.TrimPrefix(ref, "@"))
	if ref == "" {
		return "", errors.New("empty user")
	}
	if IsUserID(ref) {
		return ref, nil
	}
	if emailRE.MatchString(ref) {
		return d.userByEmail(ctx, ref)
	}
	if err := d.ensureUsers(ctx); err != nil {
		return "", err
	}
	var match []User
	for _, u := range d.Users {
		if strings.EqualFold(u.Name, ref) || strings.EqualFold(u.DisplayName, ref) || strings.EqualFold(u.RealName, ref) {
			match = append(match, u)
		}
	}
	switch len(match) {
	case 0:
		return "", fmt.Errorf("no user %q (try the id or email, or `slack-cli users find %s`)", ref, ref)
	case 1:
		return match[0].ID, nil
	}
	ids := make([]string, len(match))
	for i, u := range match {
		ids[i] = u.ID
	}
	sort.Strings(ids)
	return "", fmt.Errorf("%q matches more than one user: %s (use an id)", ref, strings.Join(ids, ", "))
}

func (d *Directory) userByEmail(ctx context.Context, email string) (string, error) {
	var resp struct {
		User rawUser `json:"user"`
	}
	if err := d.cl.CallJSON(ctx, "users.lookupByEmail", url.Values{"email": {email}}, &resp); err != nil {
		return "", err
	}
	u := resp.User.user()
	d.Users[u.ID] = u
	return u.ID, nil
}

func (d *Directory) openIM(ctx context.Context, userID string) (string, error) {
	var resp struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := d.cl.CallJSON(ctx, "conversations.open", url.Values{"users": {userID}}, &resp); err != nil {
		return "", err
	}
	return resp.Channel.ID, nil
}

func (d *Directory) ensureUsers(ctx context.Context) error {
	if len(d.Users) > 0 && time.Since(d.UsersAt) < TTL {
		return nil
	}
	page, err := d.cl.ListAll(ctx, "users.list", url.Values{"limit": {"500"}}, 0)
	if err != nil {
		return err
	}
	for _, raw := range page.Items {
		var ru rawUser
		if json.Unmarshal(raw, &ru) == nil && ru.ID != "" {
			d.Users[ru.ID] = ru.user()
		}
	}
	d.UsersAt = time.Now()
	_ = d.Save()
	return nil
}

func (d *Directory) ensureChannels(ctx context.Context) error {
	if len(d.Channels) > 0 && time.Since(d.ChannelsAt) < TTL {
		return nil
	}
	q := url.Values{"types": {"public_channel,private_channel,mpim,im"}, "exclude_archived": {"true"}, "limit": {"500"}}
	page, err := d.cl.ListAll(ctx, "conversations.list", q, 0)
	if err != nil {
		return err
	}
	for _, raw := range page.Items {
		if c, ok := parseChannel(raw); ok {
			d.Channels[c.ID] = c
		}
	}
	d.ChannelsAt = time.Now()
	_ = d.Save()
	return nil
}

// LookupUser returns a cached user by id.
func (d *Directory) LookupUser(id string) (User, bool) {
	u, ok := d.Users[id]
	return u, ok
}

// UserName returns the label of a user id, loading one user when missing.
func (d *Directory) UserName(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	if u, ok := d.Users[id]; ok {
		return u.Label()
	}
	var resp struct {
		User rawUser `json:"user"`
	}
	if d.cl.CallJSON(ctx, "users.info", url.Values{"user": {id}}, &resp) == nil && resp.User.ID != "" {
		u := resp.User.user()
		d.Users[u.ID] = u
		return u.Label()
	}
	return id
}

// User returns a user by id from the cache, or else from users.info.
func (d *Directory) User(ctx context.Context, id string) (User, bool) {
	if u, ok := d.Users[id]; ok {
		return u, true
	}
	var resp struct {
		User rawUser `json:"user"`
	}
	if d.cl.CallJSON(ctx, "users.info", url.Values{"user": {id}}, &resp) != nil || resp.User.ID == "" {
		return User{}, false
	}
	u := resp.User.user()
	d.Users[u.ID] = u
	return u, true
}

// Channel returns a conversation by id from the cache, or else from
// conversations.info.
func (d *Directory) Channel(ctx context.Context, id string) (Channel, bool) {
	if c, ok := d.Channels[id]; ok {
		return c, true
	}
	var resp struct {
		Channel json.RawMessage `json:"channel"`
	}
	if d.cl.CallJSON(ctx, "conversations.info", url.Values{"channel": {id}}, &resp) != nil {
		return Channel{}, false
	}
	c, ok := parseChannel(resp.Channel)
	if ok {
		d.Channels[c.ID] = c
	}
	return c, ok
}

// ChannelName returns the name of a channel id from the cache.
func (d *Directory) ChannelName(id string) string {
	if c, ok := d.Channels[id]; ok && c.Name != "" {
		return "#" + c.Name
	}
	return id
}

// AllUsers loads and returns every user.
func (d *Directory) AllUsers(ctx context.Context) ([]User, error) {
	if err := d.ensureUsers(ctx); err != nil {
		return nil, err
	}
	return sortUsers(d.Users), nil
}

// AllChannels loads and returns every channel.
func (d *Directory) AllChannels(ctx context.Context) ([]Channel, error) {
	if err := d.ensureChannels(ctx); err != nil {
		return nil, err
	}
	out := make([]Channel, 0, len(d.Channels))
	for _, c := range d.Channels {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func sortUsers(m map[string]User) []User {
	out := make([]User, 0, len(m))
	for _, u := range m {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label() < out[j].Label() })
	return out
}

// rawUser is the shape of a user in users.list / users.info.
type rawUser struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Real    string `json:"real_name"`
	Deleted bool   `json:"deleted"`
	IsBot   bool   `json:"is_bot"`
	Profile struct {
		RealName    string `json:"real_name"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
	} `json:"profile"`
}

func (r rawUser) user() User {
	real := r.Real
	if real == "" {
		real = r.Profile.RealName
	}
	return User{
		ID: r.ID, Name: r.Name, RealName: real, DisplayName: r.Profile.DisplayName,
		Email: r.Profile.Email, Bot: r.IsBot, Deleted: r.Deleted,
	}
}

func parseChannel(raw []byte) (Channel, bool) {
	var r struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		User      string `json:"user"`
		IsPrivate bool   `json:"is_private"`
		IsIM      bool   `json:"is_im"`
		IsMPIM    bool   `json:"is_mpim"`
	}
	if json.Unmarshal(raw, &r) != nil || r.ID == "" {
		return Channel{}, false
	}
	return Channel{ID: r.ID, Name: r.Name, User: r.User, Private: r.IsPrivate, IM: r.IsIM, MPIM: r.IsMPIM}, true
}
