package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/discord-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/discord-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/discord-cli/internal/gateway"
)

func testClient(t *testing.T) *client.Client {
	t.Helper()
	env, _ := config.ReadDotEnv(".env")
	token := cmp.Or(os.Getenv("DISCORD_BOT_TOKEN"), env["DISCORD_BOT_TOKEN"])
	if token == "" {
		t.Skip("DISCORD_BOT_TOKEN not set")
	}
	return client.New(token)
}

func testChannelID(t *testing.T) string {
	t.Helper()
	id := os.Getenv("DISCORD_TEST_CHANNEL_ID")
	if id == "" {
		t.Skip("DISCORD_TEST_CHANNEL_ID not set")
	}
	return id
}

func testGuildID(t *testing.T) string {
	t.Helper()
	id := os.Getenv("DISCORD_TEST_GUILD_ID")
	if id == "" {
		t.Skip("DISCORD_TEST_GUILD_ID not set")
	}
	return id
}

func TestAuth(t *testing.T) {
	cl := testClient(t)
	ctx := context.Background()

	t.Run("valid_token", func(t *testing.T) {
		var user client.User
		if err := cl.DoJSON(ctx, "GET", "/users/@me", nil, &user); err != nil {
			t.Fatalf("auth test failed: %v", err)
		}
		if user.ID == "" {
			t.Fatal("expected non-empty user ID")
		}
		if user.Username == "" {
			t.Fatal("expected non-empty username")
		}
		t.Logf("Authenticated as %s (ID: %s)", user.Username, user.ID)
	})

	t.Run("invalid_token", func(t *testing.T) {
		bad := client.New("invalid-token-12345")
		var user client.User
		err := bad.DoJSON(ctx, "GET", "/users/@me", nil, &user)
		if err == nil {
			t.Fatal("expected error with invalid token")
		}
		t.Logf("Got expected error: %v", err)
	})
}

func TestMessageWorkflow(t *testing.T) {
	cl := testClient(t)
	channelID := testChannelID(t)
	ctx := context.Background()

	var sentMsg client.Message

	t.Run("send", func(t *testing.T) {
		body := map[string]any{"content": "test message from Go discord-cli " + time.Now().Format(time.RFC3339)}
		if err := cl.DoJSON(ctx, "POST", "/channels/"+channelID+"/messages", body, &sentMsg); err != nil {
			t.Fatalf("send: %v", err)
		}
		if sentMsg.ID == "" {
			t.Fatal("expected message ID")
		}
		t.Logf("Sent message %s", sentMsg.ID)
	})

	t.Run("get", func(t *testing.T) {
		var msg client.Message
		path := fmt.Sprintf("/channels/%s/messages/%s", channelID, sentMsg.ID)
		if err := cl.DoJSON(ctx, "GET", path, nil, &msg); err != nil {
			t.Fatalf("get: %v", err)
		}
		if msg.ID != sentMsg.ID {
			t.Fatalf("expected ID %s, got %s", sentMsg.ID, msg.ID)
		}
	})

	t.Run("list", func(t *testing.T) {
		var msgs []client.Message
		path := fmt.Sprintf("/channels/%s/messages?limit=5", channelID)
		if err := cl.DoJSON(ctx, "GET", path, nil, &msgs); err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(msgs) == 0 {
			t.Fatal("expected at least one message")
		}
		found := false
		for _, m := range msgs {
			if m.ID == sentMsg.ID {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("sent message not found in list")
		}
	})

	t.Run("edit", func(t *testing.T) {
		path := fmt.Sprintf("/channels/%s/messages/%s", channelID, sentMsg.ID)
		body := map[string]any{"content": "edited message from Go discord-cli"}
		var msg client.Message
		if err := cl.DoJSON(ctx, "PATCH", path, body, &msg); err != nil {
			t.Fatalf("edit: %v", err)
		}
		if msg.Content != "edited message from Go discord-cli" {
			t.Fatalf("expected edited content, got %q", msg.Content)
		}
	})

	var replyMsg client.Message
	t.Run("reply", func(t *testing.T) {
		body := map[string]any{
			"content":           "reply from Go discord-cli",
			"message_reference": map[string]string{"message_id": sentMsg.ID},
		}
		if err := cl.DoJSON(ctx, "POST", "/channels/"+channelID+"/messages", body, &replyMsg); err != nil {
			t.Fatalf("reply: %v", err)
		}
		if replyMsg.MessageReference == nil || replyMsg.MessageReference.MessageID != sentMsg.ID {
			t.Fatal("expected message reference to original")
		}
	})

	t.Run("search", func(t *testing.T) {
		var msgs []client.Message
		path := fmt.Sprintf("/channels/%s/messages?limit=50", channelID)
		if err := cl.DoJSON(ctx, "GET", path, nil, &msgs); err != nil {
			t.Fatalf("search fetch: %v", err)
		}
		found := false
		for _, m := range msgs {
			if m.Content == "edited message from Go discord-cli" {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("edited message not found in search")
		}
	})

	t.Run("send_with_embed", func(t *testing.T) {
		body := map[string]any{
			"content": "embed test",
			"embeds": []map[string]any{{
				"title":       "Test Embed",
				"description": "From Go discord-cli",
				"color":       0x00ff00,
			}},
		}
		var msg client.Message
		if err := cl.DoJSON(ctx, "POST", "/channels/"+channelID+"/messages", body, &msg); err != nil {
			t.Fatalf("send with embed: %v", err)
		}
		if len(msg.Embeds) == 0 {
			t.Fatal("expected embed in message")
		}
		cl.DoNoBody(ctx, "DELETE", fmt.Sprintf("/channels/%s/messages/%s", channelID, msg.ID))
	})

	t.Run("bulk_delete", func(t *testing.T) {
		ids := []string{sentMsg.ID, replyMsg.ID}
		body := map[string]any{"messages": ids}
		path := fmt.Sprintf("/channels/%s/messages/bulk-delete", channelID)
		if err := cl.DoJSON(ctx, "POST", path, body, nil); err != nil {
			t.Fatalf("bulk delete: %v", err)
		}
		t.Logf("Bulk deleted %d messages", len(ids))
	})
}

func TestReactionWorkflow(t *testing.T) {
	cl := testClient(t)
	channelID := testChannelID(t)
	ctx := context.Background()

	body := map[string]any{"content": "reaction test from Go discord-cli"}
	var msg client.Message
	if err := cl.DoJSON(ctx, "POST", "/channels/"+channelID+"/messages", body, &msg); err != nil {
		t.Fatalf("send: %v", err)
	}
	defer cl.DoNoBody(ctx, "DELETE", fmt.Sprintf("/channels/%s/messages/%s", channelID, msg.ID))

	emoji := "%F0%9F%91%8D" // 👍 URL-encoded

	t.Run("add", func(t *testing.T) {
		path := fmt.Sprintf("/channels/%s/messages/%s/reactions/%s/@me", channelID, msg.ID, emoji)
		if err := cl.DoNoBody(ctx, "PUT", path); err != nil {
			t.Fatalf("add reaction: %v", err)
		}
	})

	t.Run("list", func(t *testing.T) {
		var fetched client.Message
		path := fmt.Sprintf("/channels/%s/messages/%s", channelID, msg.ID)
		if err := cl.DoJSON(ctx, "GET", path, nil, &fetched); err != nil {
			t.Fatalf("get message: %v", err)
		}
		if len(fetched.Reactions) == 0 {
			t.Fatal("expected reactions")
		}
	})

	t.Run("users", func(t *testing.T) {
		path := fmt.Sprintf("/channels/%s/messages/%s/reactions/%s?limit=10", channelID, msg.ID, emoji)
		var users []client.User
		if err := cl.DoJSON(ctx, "GET", path, nil, &users); err != nil {
			t.Fatalf("users: %v", err)
		}
		if len(users) == 0 {
			t.Fatal("expected at least one user")
		}
	})

	t.Run("remove", func(t *testing.T) {
		path := fmt.Sprintf("/channels/%s/messages/%s/reactions/%s/@me", channelID, msg.ID, emoji)
		if err := cl.DoNoBody(ctx, "DELETE", path); err != nil {
			t.Fatalf("remove reaction: %v", err)
		}
	})
}

func TestThreadWorkflow(t *testing.T) {
	cl := testClient(t)
	channelID := testChannelID(t)
	guildID := testGuildID(t)
	ctx := context.Background()

	body := map[string]any{"content": "thread parent from Go discord-cli"}
	var parentMsg client.Message
	if err := cl.DoJSON(ctx, "POST", "/channels/"+channelID+"/messages", body, &parentMsg); err != nil {
		t.Fatalf("send parent: %v", err)
	}
	defer cl.DoNoBody(ctx, "DELETE", fmt.Sprintf("/channels/%s/messages/%s", channelID, parentMsg.ID))

	var threadCh client.Channel

	t.Run("create", func(t *testing.T) {
		body := map[string]any{
			"name":                  "test-thread-go",
			"auto_archive_duration": 60,
		}
		path := fmt.Sprintf("/channels/%s/messages/%s/threads", channelID, parentMsg.ID)
		if err := cl.DoJSON(ctx, "POST", path, body, &threadCh); err != nil {
			t.Fatalf("create thread: %v", err)
		}
		if threadCh.ID == "" {
			t.Fatal("expected thread ID")
		}
		t.Logf("Created thread %s (ID: %s)", threadCh.Name, threadCh.ID)
	})

	t.Run("send_in_thread", func(t *testing.T) {
		body := map[string]any{"content": "hello from thread"}
		var msg client.Message
		if err := cl.DoJSON(ctx, "POST", "/channels/"+threadCh.ID+"/messages", body, &msg); err != nil {
			t.Fatalf("send in thread: %v", err)
		}
	})

	t.Run("rename", func(t *testing.T) {
		body := map[string]any{"name": "renamed-test-thread"}
		var ch client.Channel
		if err := cl.DoJSON(ctx, "PATCH", "/channels/"+threadCh.ID, body, &ch); err != nil {
			t.Fatalf("rename: %v", err)
		}
		if ch.Name != "renamed-test-thread" {
			t.Fatalf("expected renamed, got %q", ch.Name)
		}
	})

	t.Run("archive", func(t *testing.T) {
		body := map[string]any{"archived": true}
		var ch client.Channel
		if err := cl.DoJSON(ctx, "PATCH", "/channels/"+threadCh.ID, body, &ch); err != nil {
			t.Fatalf("archive: %v", err)
		}
		if ch.ThreadMetadata == nil || !ch.ThreadMetadata.Archived {
			t.Fatal("expected archived=true")
		}
	})

	t.Run("unarchive", func(t *testing.T) {
		body := map[string]any{"archived": false}
		var ch client.Channel
		if err := cl.DoJSON(ctx, "PATCH", "/channels/"+threadCh.ID, body, &ch); err != nil {
			t.Fatalf("unarchive: %v", err)
		}
	})

	t.Run("list_active", func(t *testing.T) {
		var resp client.ActiveThreadsResponse
		if err := cl.DoJSON(ctx, "GET", "/guilds/"+guildID+"/threads/active", nil, &resp); err != nil {
			t.Fatalf("list active: %v", err)
		}
		t.Logf("Found %d active threads", len(resp.Threads))
	})

	t.Cleanup(func() {
		cl.DoNoBody(ctx, "DELETE", "/channels/"+threadCh.ID)
	})
}

func TestChannelWorkflow(t *testing.T) {
	cl := testClient(t)
	guildID := testGuildID(t)
	channelID := testChannelID(t)
	ctx := context.Background()

	t.Run("info", func(t *testing.T) {
		var ch client.Channel
		if err := cl.DoJSON(ctx, "GET", "/channels/"+channelID, nil, &ch); err != nil {
			t.Fatalf("info: %v", err)
		}
		if ch.ID != channelID {
			t.Fatalf("expected ID %s, got %s", channelID, ch.ID)
		}
		t.Logf("Channel: %s (%s)", ch.Name, client.ChannelTypeName(ch.Type))
	})

	t.Run("list", func(t *testing.T) {
		var channels []client.Channel
		if err := cl.DoJSON(ctx, "GET", "/guilds/"+guildID+"/channels", nil, &channels); err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(channels) == 0 {
			t.Fatal("expected at least one channel")
		}
		t.Logf("Found %d channels", len(channels))
	})

	var createdCh client.Channel
	t.Run("create", func(t *testing.T) {
		body := map[string]any{
			"name":  "test-go-discord-cli",
			"type":  0,
			"topic": "test channel from Go discord-cli",
		}
		if err := cl.DoJSON(ctx, "POST", "/guilds/"+guildID+"/channels", body, &createdCh); err != nil {
			t.Fatalf("create: %v", err)
		}
		if createdCh.ID == "" {
			t.Fatal("expected channel ID")
		}
		t.Logf("Created channel %s (ID: %s)", createdCh.Name, createdCh.ID)
	})

	t.Run("edit", func(t *testing.T) {
		body := map[string]any{"topic": "updated topic"}
		var ch client.Channel
		if err := cl.DoJSON(ctx, "PATCH", "/channels/"+createdCh.ID, body, &ch); err != nil {
			t.Fatalf("edit: %v", err)
		}
		if ch.Topic != "updated topic" {
			t.Fatalf("expected updated topic, got %q", ch.Topic)
		}
	})

	t.Run("delete", func(t *testing.T) {
		if err := cl.DoNoBody(ctx, "DELETE", "/channels/"+createdCh.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		t.Logf("Deleted channel %s", createdCh.ID)
	})
}

func TestGateway(t *testing.T) {
	env, _ := config.ReadDotEnv(".env")
	token := cmp.Or(os.Getenv("DISCORD_BOT_TOKEN"), env["DISCORD_BOT_TOKEN"])
	if token == "" {
		t.Skip("DISCORD_BOT_TOKEN not set")
	}

	intents := (1 << 0) | (1 << 9) | (1 << 15) // GUILDS + GUILD_MESSAGES + MESSAGE_CONTENT

	t.Run("connect_ready", func(t *testing.T) {
		gw := gateway.New(token, intents)
		ready, err := gw.Connect()
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer gw.Close()

		if ready.SessionID == "" {
			t.Fatal("expected session ID")
		}
		if ready.User.ID == "" {
			t.Fatal("expected user ID in READY")
		}
		t.Logf("Gateway READY: user=%s session=%s guilds=%d",
			ready.User.Username, ready.SessionID, len(ready.Guilds))
	})

	t.Run("message_event", func(t *testing.T) {
		if testing.Short() {
			t.Skip("skipping gateway message test in short mode")
		}
		channelID := os.Getenv("DISCORD_TEST_CHANNEL_ID")
		if channelID == "" {
			t.Skip("DISCORD_TEST_CHANNEL_ID not set")
		}

		gw := gateway.New(token, intents)
		ready, err := gw.Connect()
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer gw.Close()
		t.Logf("Connected as %s", ready.User.Username)

		received := make(chan string, 1)
		go gw.Listen(func(eventType string, data json.RawMessage) {
			if eventType == "MESSAGE_CREATE" {
				var msg client.Message
				json.Unmarshal(data, &msg)
				received <- msg.Content
			}
		})

		time.Sleep(500 * time.Millisecond)
		cl := client.New(token)
		testContent := fmt.Sprintf("gateway test %d", time.Now().UnixNano())
		body := map[string]any{"content": testContent}
		var msg client.Message
		if err := cl.DoJSON(context.Background(), "POST", "/channels/"+channelID+"/messages", body, &msg); err != nil {
			t.Fatalf("send REST message: %v", err)
		}
		defer cl.DoNoBody(context.Background(), "DELETE", fmt.Sprintf("/channels/%s/messages/%s", channelID, msg.ID))

		select {
		case content := <-received:
			if content != testContent {
				t.Fatalf("expected %q, got %q", testContent, content)
			}
			t.Log("Received MESSAGE_CREATE via gateway")
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for MESSAGE_CREATE")
		}
	})
}
