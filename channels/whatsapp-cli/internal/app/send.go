package app

import (
	"context"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trebi-ai/trebi-connectors/channels/whatsapp-cli/internal/store"
	"github.com/trebi-ai/trebi-connectors/channels/whatsapp-cli/internal/wa"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// OutFile is one file to send. Data, when set, replaces the contents of
// Path. Reply, when set, quotes a message.
type OutFile struct {
	Path    string
	Data    []byte
	Name    string
	Caption string
	Mime    string
	Reply   *waProto.ContextInfo
}

// SendFile uploads and sends a file message and records it in the store.
// It returns the message id and the file meta.
func (a *App) SendFile(ctx context.Context, to types.JID, f OutFile) (string, map[string]string, error) {
	data := f.Data
	if data == nil {
		var err error
		if data, err = os.ReadFile(f.Path); err != nil {
			return "", nil, err
		}
	}
	filePath, filename, caption, mimeOverride := f.Path, f.Name, f.Caption, f.Mime
	if filePath == "" {
		filePath = filename
	}
	name := strings.TrimSpace(filename)
	if name == "" {
		name = filepath.Base(filePath)
	}
	mimeType := strings.TrimSpace(mimeOverride)
	if mimeType == "" {
		// Use filePath for MIME detection, not the display name override
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(filePath)))
	}
	if mimeType == "" {
		sniff := data
		if len(sniff) > 512 {
			sniff = sniff[:512]
		}
		mimeType = http.DetectContentType(sniff)
	}

	mediaType := "document"
	uploadType, _ := wa.MediaTypeFromString("document")
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		mediaType = "image"
		uploadType, _ = wa.MediaTypeFromString("image")
	case strings.HasPrefix(mimeType, "video/"):
		mediaType = "video"
		uploadType, _ = wa.MediaTypeFromString("video")
	case strings.HasPrefix(mimeType, "audio/"):
		mediaType = "audio"
		uploadType, _ = wa.MediaTypeFromString("audio")
	}

	up, err := a.wa.Upload(ctx, data, uploadType)
	if err != nil {
		return "", nil, err
	}

	now := time.Now().UTC()
	msg := &waProto.Message{}

	switch mediaType {
	case "image":
		msg.ImageMessage = &waProto.ImageMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			Caption:       proto.String(caption),
		}
	case "video":
		msg.VideoMessage = &waProto.VideoMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			Caption:       proto.String(caption),
		}
	case "audio":
		msg.AudioMessage = &waProto.AudioMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			PTT:           proto.Bool(false),
		}
	default:
		msg.DocumentMessage = &waProto.DocumentMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			FileName:      proto.String(name),
			Caption:       proto.String(caption),
			Title:         proto.String(name),
		}
	}

	if f.Reply != nil {
		switch {
		case msg.ImageMessage != nil:
			msg.ImageMessage.ContextInfo = f.Reply
		case msg.VideoMessage != nil:
			msg.VideoMessage.ContextInfo = f.Reply
		case msg.AudioMessage != nil:
			msg.AudioMessage.ContextInfo = f.Reply
		default:
			msg.DocumentMessage.ContextInfo = f.Reply
		}
	}

	id, err := a.wa.SendProtoMessage(ctx, to, msg)
	if err != nil {
		return "", nil, err
	}

	chatName := a.wa.ResolveChatName(ctx, to, "")
	kind := chatKind(to)
	_ = a.db.UpsertChat(to.String(), kind, chatName, now)
	_ = a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:       to.String(),
		ChatName:      chatName,
		MsgID:         id,
		SenderJID:     "",
		SenderName:    "me",
		Timestamp:     now,
		FromMe:        true,
		Text:          caption,
		MediaType:     mediaType,
		MediaCaption:  caption,
		Filename:      name,
		MimeType:      mimeType,
		DirectPath:    up.DirectPath,
		MediaKey:      up.MediaKey,
		FileSHA256:    up.FileSHA256,
		FileEncSHA256: up.FileEncSHA256,
		FileLength:    up.FileLength,
	})

	return id, map[string]string{
		"name":      name,
		"mime_type": mimeType,
		"media":     mediaType,
	}, nil
}
