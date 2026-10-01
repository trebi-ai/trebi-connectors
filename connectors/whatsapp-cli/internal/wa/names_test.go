package wa

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

var testGroup = types.JID{User: "120363000000000001", Server: types.GroupServer}

type fakeGroups struct {
	connected bool
	info      *types.GroupInfo
	err       error
	calls     int
}

func (f *fakeGroups) IsConnected() bool { return f.connected }

func (f *fakeGroups) GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error) {
	f.calls++
	return f.info, f.err
}

func TestGroupNamesResolve(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	g := newGroupNames()
	g.now = func() time.Time { return now }

	src := &fakeGroups{}
	if name := g.resolve(ctx, src, testGroup); name != "" || src.calls != 0 {
		t.Fatalf("not connected: %q, %d calls", name, src.calls)
	}

	src.connected, src.err = true, errors.New("timeout")
	if name := g.resolve(ctx, src, testGroup); name != "" || src.calls != 1 {
		t.Fatalf("failure: %q, %d calls", name, src.calls)
	}
	if name := g.resolve(ctx, src, testGroup); name != "" || src.calls != 1 {
		t.Fatalf("a kept failure must not call again: %q, %d calls", name, src.calls)
	}

	now = now.Add(groupFailTTL + time.Second)
	src.err, src.info = nil, &types.GroupInfo{GroupName: types.GroupName{Name: "Family"}}
	if name := g.resolve(ctx, src, testGroup); name != "Family" || src.calls != 2 {
		t.Fatalf("retry: %q, %d calls", name, src.calls)
	}
	now = now.Add(groupNameTTL - time.Minute)
	if name := g.resolve(ctx, src, testGroup); name != "Family" || src.calls != 2 {
		t.Fatalf("cache: %q, %d calls", name, src.calls)
	}
	now = now.Add(2 * time.Minute)
	if g.resolve(ctx, src, testGroup); src.calls != 3 {
		t.Fatalf("an old entry must call again: %d calls", src.calls)
	}
}

func TestResolveChatName(t *testing.T) {
	ctx := context.Background()
	c, err := New(Options{StorePath: filepath.Join(t.TempDir(), "session.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseStore() })

	// The client is not connected, so a group lookup fails.
	if name := c.ResolveChatName(ctx, testGroup, "Sam"); name != "" {
		t.Fatalf("a group must not take the sender name or the JID: %q", name)
	}
	c.RememberGroup(testGroup, "Family")
	if name := c.ResolveChatName(ctx, testGroup, "Sam"); name != "Family" {
		t.Fatalf("cached group: %q", name)
	}
	if name := c.KnownChatName(ctx, testGroup, ""); name != "Family" {
		t.Fatalf("known group: %q", name)
	}

	dm := types.JID{User: "5511999999999", Server: types.DefaultUserServer}
	if name := c.ResolveChatName(ctx, dm, "Sam"); name != "Sam" {
		t.Fatalf("dm push name: %q", name)
	}
	if name := c.ResolveChatName(ctx, dm, ""); name != "" {
		t.Fatalf("dm with no name: %q", name)
	}
}
