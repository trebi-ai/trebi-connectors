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
