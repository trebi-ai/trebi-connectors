package main

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
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
