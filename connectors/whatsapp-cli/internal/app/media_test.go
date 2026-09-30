package app

import (
	"context"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/wa/fakewa"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/store"
)

func TestDownloadMediaJobMarksDownloaded(t *testing.T) {
	a := newTestApp(t)
	f := fakewa.New()
	a.wa = f

	chat := "123@s.whatsapp.net"
	if err := a.db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:       chat,
		MsgID:         "mid",
		SenderJID:     chat,
		SenderName:    "Alice",
		Timestamp:     time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
		FromMe:        false,
		Text:          "",
		MediaType:     "image",
		MediaCaption:  "cap",
		Filename:      "pic.jpg",
		MimeType:      "image/jpeg",
		DirectPath:    "/direct/path",
		MediaKey:      []byte{1, 2, 3},
		FileSHA256:    []byte{4, 5},
		FileEncSHA256: []byte{6, 7},
		FileLength:    123,
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	if err := a.downloadMediaJob(context.Background(), mediaJob{chatJID: chat, msgID: "mid"}); err != nil {
		t.Fatalf("downloadMediaJob: %v", err)
	}

	info, err := a.db.GetMediaDownloadInfo(chat, "mid")
	if err != nil {
		t.Fatalf("GetMediaDownloadInfo: %v", err)
	}
	if info.LocalPath == "" || filepath.IsAbs(info.LocalPath) {
		t.Fatalf("LocalPath %q, want a path relative to the store", info.LocalPath)
	}
	if _, err := os.Stat(a.MediaPath(info.LocalPath)); err != nil {
		t.Fatalf("expected downloaded file to exist: %v", err)
	}
}
