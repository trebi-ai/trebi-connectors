package cmd

import (
	"encoding/json"
	"testing"
)

func TestIsThreadChannelType(t *testing.T) {
	for _, tt := range []struct {
		typ  int
		want bool
	}{
		{0, false},
		{2, false},
		{10, true},
		{11, true},
		{12, true},
		{15, false},
	} {
		if got := isThreadChannelType(tt.typ); got != tt.want {
			t.Errorf("isThreadChannelType(%d)=%v want %v", tt.typ, got, tt.want)
		}
	}
}

func TestCategoryAllowed(t *testing.T) {
	cats := []string{"messages", "threads"}
	if !categoryAllowed("MESSAGE_CREATE", false, cats) {
		t.Error("MESSAGE_CREATE should match messages")
	}
	if !categoryAllowed("THREAD_CREATE", false, cats) {
		t.Error("THREAD_CREATE should match threads")
	}
	if categoryAllowed("VOICE_STATE_UPDATE", false, cats) {
		t.Error("VOICE_STATE_UPDATE should not match")
	}
	if categoryAllowed("UNKNOWN_EVENT", false, cats) {
		t.Error("unknown event should not match when filtering")
	}
	if !categoryAllowed("UNKNOWN_EVENT", true, nil) {
		t.Error("allEvents should allow unknown types")
	}
	if !categoryAllowed("THREAD_MEMBER_UPDATE", false, []string{"threads"}) {
		t.Error("THREAD_MEMBER_UPDATE should be threads")
	}
}

func TestThreadParentCacheObserve(t *testing.T) {
	cache := newThreadParentCache(nil)
	parent := "1502847711018356766"
	thread := "999000111222333444"

	cache.observe("THREAD_CREATE", json.RawMessage(`{
		"id":"`+thread+`","type":11,"guild_id":"g1","parent_id":"`+parent+`"
	}`))
	if p, ok := cache.parentOf(thread); !ok || p != parent {
		t.Fatalf("after THREAD_CREATE: parentOf=%q ok=%v want %q", p, ok, parent)
	}

	cache.observe("THREAD_LIST_SYNC", json.RawMessage(`{
		"guild_id":"g1",
		"channel_ids":["`+parent+`"],
		"threads":[
			{"id":"t2","parent_id":"`+parent+`","type":11},
			{"id":"t3","parent_id":"other","type":11}
		]
	}`))
	if p, ok := cache.parentOf("t2"); !ok || p != parent {
		t.Fatalf("THREAD_LIST_SYNC t2: got %q ok=%v", p, ok)
	}
	if p, ok := cache.parentOf("t3"); !ok || p != "other" {
		t.Fatalf("THREAD_LIST_SYNC t3: got %q ok=%v", p, ok)
	}

	// CHANNEL_* path for thread types
	cache.observe("CHANNEL_CREATE", json.RawMessage(`{
		"id":"t4","type":12,"guild_id":"g1","parent_id":"`+parent+`"
	}`))
	if p, ok := cache.parentOf("t4"); !ok || p != parent {
		t.Fatalf("CHANNEL_CREATE thread: got %q ok=%v", p, ok)
	}

	// Non-thread CHANNEL_CREATE should not pollute map
	cache.observe("CHANNEL_CREATE", json.RawMessage(`{
		"id":"text1","type":0,"guild_id":"g1","parent_id":"cat"
	}`))
	if _, ok := cache.parentOf("text1"); ok {
		t.Fatal("text channel should not be in parent map")
	}

	cache.observe("THREAD_DELETE", json.RawMessage(`{"id":"`+thread+`","type":11,"parent_id":"`+parent+`"}`))
	if _, ok := cache.parentOf(thread); ok {
		t.Fatal("THREAD_DELETE should remove thread from map")
	}

	cache.observe("CHANNEL_DELETE", json.RawMessage(`{"id":"t4","type":12,"parent_id":"`+parent+`"}`))
	if _, ok := cache.parentOf("t4"); ok {
		t.Fatal("CHANNEL_DELETE of thread should remove from map")
	}
}

func TestMatchesChannel(t *testing.T) {
	cache := newThreadParentCache(nil)
	parent := "parent-ch"
	thread := "thread-1"
	cache.set(thread, parent)

	if !cache.matchesChannel(parent, parent) {
		t.Error("parent channel should match itself")
	}
	if !cache.matchesChannel(thread, parent) {
		t.Error("child thread should match parent filter")
	}
	if cache.matchesChannel("unrelated", parent) {
		t.Error("unrelated channel should not match")
	}
	if cache.matchesChannel(thread, "other-parent") {
		t.Error("thread under other parent should not match")
	}
	// no filter
	if !cache.matchesChannel("anything", "") {
		t.Error("empty filter should always match")
	}
}

func TestEventPassesFilters_MessagesAndThreads(t *testing.T) {
	cache := newThreadParentCache(nil)
	parent := "1502847711018356766"
	thread := "thread-abc"
	guild := "1053623890653499413"
	cache.set(thread, parent)

	msgInParent := json.RawMessage(`{
		"id":"m1","channel_id":"` + parent + `","guild_id":"` + guild + `",
		"author":{"id":"u1","bot":false},"content":"hi"
	}`)
	msgInThread := json.RawMessage(`{
		"id":"m2","channel_id":"` + thread + `","guild_id":"` + guild + `",
		"author":{"id":"u1","bot":false},"content":"in thread"
	}`)
	msgOther := json.RawMessage(`{
		"id":"m3","channel_id":"other-ch","guild_id":"` + guild + `",
		"author":{"id":"u1","bot":false},"content":"nope"
	}`)
	msgBot := json.RawMessage(`{
		"id":"m4","channel_id":"` + parent + `","guild_id":"` + guild + `",
		"author":{"id":"bot1","bot":true},"content":"beep"
	}`)
	msgOtherGuild := json.RawMessage(`{
		"id":"m5","channel_id":"` + parent + `","guild_id":"other-guild",
		"author":{"id":"u1","bot":false},"content":"x"
	}`)
	threadCreate := json.RawMessage(`{
		"id":"` + thread + `","type":11,"guild_id":"` + guild + `","parent_id":"` + parent + `","name":"t"
	}`)
	threadCreateOther := json.RawMessage(`{
		"id":"t-other","type":11,"guild_id":"` + guild + `","parent_id":"other-parent","name":"t"
	}`)
	listSync := json.RawMessage(`{
		"guild_id":"` + guild + `",
		"channel_ids":["` + parent + `"],
		"threads":[{"id":"` + thread + `","parent_id":"` + parent + `","type":11}]
	}`)
	listSyncMiss := json.RawMessage(`{
		"guild_id":"` + guild + `",
		"channel_ids":["unrelated"],
		"threads":[{"id":"t9","parent_id":"p9","type":11}]
	}`)
	memberUpdate := json.RawMessage(`{
		"id":"` + thread + `","guild_id":"` + guild + `","user_id":"u1"
	}`)
	reactionInThread := json.RawMessage(`{
		"user_id":"u1","channel_id":"` + thread + `","guild_id":"` + guild + `",
		"message_id":"m2","emoji":{"name":"👍"}
	}`)

	tests := []struct {
		name        string
		eventType   string
		data        json.RawMessage
		server      string
		channel     string
		includeBots bool
		want        bool
	}{
		{"msg parent", "MESSAGE_CREATE", msgInParent, guild, parent, false, true},
		{"msg child thread", "MESSAGE_CREATE", msgInThread, guild, parent, false, true},
		{"msg unrelated channel", "MESSAGE_CREATE", msgOther, guild, parent, false, false},
		{"msg bot filtered", "MESSAGE_CREATE", msgBot, guild, parent, false, false},
		{"msg bot included", "MESSAGE_CREATE", msgBot, guild, parent, true, true},
		{"msg wrong guild", "MESSAGE_CREATE", msgOtherGuild, guild, parent, false, false},
		{"msg no channel filter", "MESSAGE_CREATE", msgOther, guild, "", false, true},
		{"msg no filters", "MESSAGE_CREATE", msgInParent, "", "", false, true},
		{"thread create under parent", "THREAD_CREATE", threadCreate, guild, parent, false, true},
		{"thread create other parent", "THREAD_CREATE", threadCreateOther, guild, parent, false, false},
		{"thread delete under parent", "THREAD_DELETE", threadCreate, guild, parent, false, true},
		{"thread list sync match", "THREAD_LIST_SYNC", listSync, guild, parent, false, true},
		{"thread list sync miss", "THREAD_LIST_SYNC", listSyncMiss, guild, parent, false, false},
		{"thread member update", "THREAD_MEMBER_UPDATE", memberUpdate, guild, parent, false, true},
		{"reaction in thread", "MESSAGE_REACTION_ADD", reactionInThread, guild, parent, false, true},
		// thread events have no author — bot filter must not drop them
		{"thread no bot field", "THREAD_CREATE", threadCreate, guild, parent, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eventPassesFilters(tt.eventType, tt.data, tt.server, tt.channel, tt.includeBots, cache)
			if got != tt.want {
				t.Errorf("eventPassesFilters=%v want %v", got, tt.want)
			}
		})
	}
}

func TestChannelFilterMatch_NilCache(t *testing.T) {
	// Without cache, only exact channel_id equality (legacy-ish path).
	data := json.RawMessage(`{"channel_id":"c1","guild_id":"g1"}`)
	if !channelFilterMatch("MESSAGE_CREATE", data, "c1", "c1", nil) {
		t.Error("exact match should pass with nil cache")
	}
	if channelFilterMatch("MESSAGE_CREATE", data, "thread1", "c1", nil) {
		t.Error("thread id should not match parent without cache")
	}
}

func TestEventCategoryMapThreads(t *testing.T) {
	want := map[string]string{
		"THREAD_CREATE":        "threads",
		"THREAD_UPDATE":        "threads",
		"THREAD_DELETE":        "threads",
		"THREAD_LIST_SYNC":     "threads",
		"THREAD_MEMBER_UPDATE": "threads",
	}
	for ev, cat := range want {
		if eventCategoryMap[ev] != cat {
			t.Errorf("eventCategoryMap[%s]=%q want %q", ev, eventCategoryMap[ev], cat)
		}
	}
	if _, ok := intentBits["threads"]; !ok {
		t.Error("intentBits should include threads")
	}
}

func TestEnrichParentID(t *testing.T) {
	cache := newThreadParentCache(nil)
	parent := "parent-ch"
	thread := "thread-1"
	cache.set(thread, parent)

	msgThread := json.RawMessage(`{"id":"m1","channel_id":"` + thread + `","guild_id":"g1","content":"hi","author":{"id":"u1","bot":false}}`)
	msgParent := json.RawMessage(`{"id":"m2","channel_id":"` + parent + `","guild_id":"g1","content":"hi"}`)
	msgUnknown := json.RawMessage(`{"id":"m3","channel_id":"other","guild_id":"g1","content":"x"}`)
	msgExistingParent := json.RawMessage(`{"id":"m4","channel_id":"` + thread + `","parent_id":"keep-me","content":"x"}`)
	reaction := json.RawMessage(`{"user_id":"u1","channel_id":"` + thread + `","message_id":"m1","emoji":{"name":"👍"}}`)
	threadCreate := json.RawMessage(`{"id":"` + thread + `","parent_id":"` + parent + `","type":11}`)

	decode := func(raw json.RawMessage) map[string]any {
		t.Helper()
		var d map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}

	got := enrichParentID("MESSAGE_CREATE", msgThread, cache)
	d := decode(got)
	if d["parent_id"] != parent {
		t.Errorf("MESSAGE_CREATE thread: parent_id=%v want %q", d["parent_id"], parent)
	}
	if d["channel_id"] != thread {
		t.Errorf("channel_id should stay thread id, got %v", d["channel_id"])
	}
	if d["content"] != "hi" {
		t.Errorf("content should be preserved, got %v", d["content"])
	}

	d = decode(enrichParentID("MESSAGE_CREATE", msgParent, cache))
	if _, ok := d["parent_id"]; ok {
		t.Errorf("parent channel message should not get parent_id, got %v", d["parent_id"])
	}

	d = decode(enrichParentID("MESSAGE_CREATE", msgUnknown, cache))
	if _, ok := d["parent_id"]; ok {
		t.Errorf("unknown channel should not invent parent_id")
	}

	d = decode(enrichParentID("MESSAGE_CREATE", msgExistingParent, cache))
	if d["parent_id"] != "keep-me" {
		t.Errorf("must not overwrite existing parent_id, got %v", d["parent_id"])
	}

	d = decode(enrichParentID("MESSAGE_REACTION_ADD", reaction, cache))
	if d["parent_id"] != parent {
		t.Errorf("reaction in thread: parent_id=%v want %q", d["parent_id"], parent)
	}

	// THREAD_* already has parent_id from Discord — leave payload unchanged (byte-equal path is fine).
	got = enrichParentID("THREAD_CREATE", threadCreate, cache)
	if string(got) != string(threadCreate) {
		t.Errorf("THREAD_CREATE should not be rewritten")
	}

	// nil cache
	got = enrichParentID("MESSAGE_CREATE", msgThread, nil)
	if string(got) != string(msgThread) {
		t.Errorf("nil cache should pass through")
	}
}
