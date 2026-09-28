package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// ceMessage builds a classified "message" event for chat/sender.
func ceMessage(chat, sender string, fromMe bool) classifiedEvent {
	return classifiedEvent{
		name: "message", category: "messages",
		chatJID: chat, senderJID: sender, isFromMe: fromMe,
		payload: map[string]any{"chat": chat, "sender": sender},
	}
}

func TestRenderFilters(t *testing.T) {
	ce := ceMessage("chatA", "userX", false)

	// Category mismatch drops.
	if _, ok := ce.render(ListenFilter{Categories: map[string]bool{"receipts": true}}); ok {
		t.Fatal("expected category mismatch to drop")
	}
	// Chat filter mismatch drops.
	if _, ok := ce.render(ListenFilter{Categories: map[string]bool{"messages": true}, ChatFilter: "chatB"}); ok {
		t.Fatal("expected chat mismatch to drop")
	}
	// Match passes.
	line, ok := ce.render(ListenFilter{Categories: map[string]bool{"messages": true}, ChatFilter: "chatA"})
	if !ok {
		t.Fatal("expected match")
	}
	var got map[string]any
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["t"] != "message" {
		t.Fatalf("t = %v", got["t"])
	}
}

func TestSubscriberSetPerFilterFanout(t *testing.T) {
	set := NewSubscriberSet()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var bufA, bufB bytes.Buffer
	var wg sync.WaitGroup

	// Subscriber A: only chatA. Subscriber B: only chatB.
	start := func(buf *bytes.Buffer, chat string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bw := bufio.NewWriter(buf)
			_ = set.Serve(ctx, ListenFilter{
				Categories: map[string]bool{"messages": true},
				ChatFilter: chat,
			}, bw, bw.Flush)
		}()
	}
	start(&bufA, "chatA")
	start(&bufB, "chatB")

	// Wait for both subscribers to register.
	deadline := time.Now().Add(2 * time.Second)
	for set.Count() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("subscribers did not register")
		}
		time.Sleep(2 * time.Millisecond)
	}

	set.dispatch(ceMessage("chatA", "u1", false))
	set.dispatch(ceMessage("chatB", "u2", false))
	set.dispatch(ceMessage("chatA", "u3", false))

	// Let the writer goroutines drain.
	time.Sleep(50 * time.Millisecond)
	cancel()
	wg.Wait()

	countLines := func(b *bytes.Buffer, wantChat string) int {
		n := 0
		for _, ln := range strings.Split(strings.TrimSpace(b.String()), "\n") {
			if ln == "" {
				continue
			}
			if !strings.Contains(ln, wantChat) {
				t.Fatalf("line for %s leaked wrong chat: %s", wantChat, ln)
			}
			n++
		}
		return n
	}

	if got := countLines(&bufA, "chatA"); got != 2 {
		t.Fatalf("subscriber A got %d lines, want 2", got)
	}
	if got := countLines(&bufB, "chatB"); got != 1 {
		t.Fatalf("subscriber B got %d lines, want 1", got)
	}
}

func TestSubscriberSetRemoveOnCancel(t *testing.T) {
	set := NewSubscriberSet()
	ctx, cancel := context.WithCancel(context.Background())

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		bw := bufio.NewWriter(&buf)
		_ = set.Serve(ctx, ListenFilter{Categories: map[string]bool{"messages": true}}, bw, bw.Flush)
		close(done)
	}()

	for set.Count() != 1 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if set.Count() != 0 {
		t.Fatalf("subscriber not removed after cancel: count=%d", set.Count())
	}
}
