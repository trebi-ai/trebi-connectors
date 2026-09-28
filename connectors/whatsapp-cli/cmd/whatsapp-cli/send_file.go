package main

import (
	"context"

	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/app"
	"go.mau.fi/whatsmeow/types"
)

func sendFile(ctx context.Context, a sendApp, to types.JID, filePath, filename, caption, mimeOverride string) (string, map[string]string, error) {
	return a.SendFile(ctx, to, app.OutFile{Path: filePath, Name: filename, Caption: caption, Mime: mimeOverride})
}

func chatKindFromJID(j types.JID) string {
	if j.Server == types.GroupServer {
		return "group"
	}
	if j.IsBroadcastList() {
		return "broadcast"
	}
	if j.Server == types.DefaultUserServer {
		return "dm"
	}
	return "unknown"
}
