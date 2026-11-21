package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/ipc"
)

func TestParseChatPresenceFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		state     string
		media     string
		wantState types.ChatPresence
		wantMedia types.ChatPresenceMedia
		wantS     string
		wantM     string
		wantErr   bool
	}{
		{
			name:      "defaults",
			wantState: types.ChatPresenceComposing,
			wantMedia: types.ChatPresenceMediaText,
			wantS:     "composing",
			wantM:     "text",
		},
		{
			name:      "paused audio",
			state:     "paused",
			media:     "audio",
			wantState: types.ChatPresencePaused,
			wantMedia: types.ChatPresenceMediaAudio,
			wantS:     "paused",
			wantM:     "audio",
		},
		{
			name:      "case insensitive",
			state:     "Composing",
			media:     "AUDIO",
			wantState: types.ChatPresenceComposing,
			wantMedia: types.ChatPresenceMediaAudio,
			wantS:     "composing",
			wantM:     "audio",
		},
		{name: "bad state", state: "typing", wantErr: true},
		{name: "bad media", media: "video", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cp, media, s, m, err := parseChatPresenceFlags(tt.state, tt.media)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cp != tt.wantState || media != tt.wantMedia || s != tt.wantS || m != tt.wantM {
				t.Fatalf("got state=%q media=%q s=%q m=%q", cp, media, s, m)
			}
		})
	}
}

func TestParseReceiptType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    types.ReceiptType
		wantOut string
		wantErr bool
	}{
		{in: "", want: types.ReceiptTypeRead, wantOut: "read"},
		{in: "read", want: types.ReceiptTypeRead, wantOut: "read"},
		{in: "PLAYED", want: types.ReceiptTypePlayed, wantOut: "played"},
		{in: "delivered", wantErr: true},
		{in: "seen", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in+"_", func(t *testing.T) {
			t.Parallel()
			rt, out, err := parseReceiptType(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if rt != tt.want || out != tt.wantOut {
				t.Fatalf("got rt=%q out=%q", rt, out)
			}
		})
	}
}

func TestParsePresenceState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    types.Presence
		wantOut string
		wantErr bool
	}{
		{in: "available", want: types.PresenceAvailable, wantOut: "available"},
		{in: "UNAVAILABLE", want: types.PresenceUnavailable, wantOut: "unavailable"},
		{in: "", wantErr: true},
		{in: "online", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in+"_", func(t *testing.T) {
			t.Parallel()
			p, out, err := parsePresenceState(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p != tt.want || out != tt.wantOut {
				t.Fatalf("got p=%q out=%q", p, out)
			}
		})
	}
}

func TestNormalizeMsgIDs(t *testing.T) {
	t.Parallel()

	ids, err := normalizeMsgIDs([]string{" a ", "", "b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("got %v", ids)
	}
	if _, err := normalizeMsgIDs(nil); err == nil {
		t.Fatal("expected error for empty ids")
	}
}

func TestDispatchMedia(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "wa-dispatch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, err := ipc.Serve(ctx, dir, func(_ context.Context, req ipc.Request) ipc.Response {
		return ipc.Response{OK: true, Chat: req.Chat, ID: req.MsgID, Path: "/m/a.jpg", Bytes: 7, MediaType: "image", MimeType: "image/jpeg"}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = stdout }()

	f := &rootFlags{storeDir: dir, asJSON: true}
	req := ipc.Request{Cmd: "media_download", Chat: "1@s.whatsapp.net", MsgID: "M1"}
	err = f.dispatch(req, func() error {
		t.Fatal("direct path must not run")
		return nil
	})
	w.Close()
	os.Stdout = stdout
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("output %q: %v", b, err)
	}
	got := env.Data
	want := map[string]any{"chat": "1@s.whatsapp.net", "id": "M1", "path": "/m/a.jpg", "bytes": float64(7), "media_type": "image", "mime_type": "image/jpeg", "downloaded": true}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}
