package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	cli "github.com/urfave/cli/v3"

	"github.com/flarco/cli-tools/slack-cli/internal/client"
	"github.com/flarco/cli-tools/slack-cli/internal/output"
)

// HelperCommands returns hand-written leaves added to the method tree.
func HelperCommands() map[string][]*cli.Command {
	return map[string][]*cli.Command{
		"files":         {filesUploadCommand()},
		"conversations": {conversationsFindCommand(), conversationsReadCommand()},
		"users":         {usersFindCommand()},
		"canvases":      {canvasesReadCommand()},
	}
}

// filesUploadCommand uploads a file with the external-upload flow that
// replaced files.upload.
func filesUploadCommand() *cli.Command {
	return &cli.Command{
		Name:      "upload",
		Usage:     "Upload a file (getUploadURLExternal + completeUploadExternal)",
		ArgsUsage: "<path>",
		Description: `Uploads a local file with Slack's current flow: get an upload URL, POST the
bytes, then complete the upload (optionally sharing it to a channel).`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "channel", Aliases: []string{"c"}, Usage: "Share to this channel (id, #name, @user, or email)"},
			&cli.StringFlag{Name: "title", Usage: "File title (default: the file name)"},
			&cli.StringFlag{Name: "filename", Usage: "File name shown in Slack (default: the base name)"},
			&cli.StringFlag{Name: "thread-ts", Usage: "Share as a reply to this thread"},
			&cli.StringFlag{Name: "initial-comment", Usage: "Message posted with the file"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			path := c.Args().First()
			if path == "" || c.Args().Len() > 1 {
				return fmt.Errorf("expected one <path>")
			}
			fi, err := os.Stat(path)
			if err != nil {
				return err
			}
			cl, err := a.Client()
			if err != nil {
				return err
			}
			filename := c.String("filename")
			if filename == "" {
				filename = baseName(path)
			}
			// 1. Get an upload URL.
			var up struct {
				UploadURL string `json:"upload_url"`
				FileID    string `json:"file_id"`
			}
			q := url.Values{"filename": {filename}, "length": {strconv.FormatInt(fi.Size(), 10)}}
			if err := cl.CallJSON(ctx, "files.getUploadURLExternal", q, &up); err != nil {
				return err
			}
			// 2. POST the bytes.
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := cl.Upload(ctx, up.UploadURL, f, fi.Size()); err != nil {
				return err
			}
			// 3. Complete the upload.
			title := c.String("title")
			if title == "" {
				title = filename
			}
			files, _ := json.Marshal([]map[string]string{{"id": up.FileID, "title": title}})
			done := url.Values{"files": {string(files)}}
			if ch := c.String("channel"); ch != "" {
				id, err := resolveChannel(ctx, a, ch)
				if err != nil {
					return err
				}
				done.Set("channel_id", id)
			}
			if v := c.String("thread-ts"); v != "" {
				done.Set("thread_ts", v)
			}
			if v := c.String("initial-comment"); v != "" {
				done.Set("initial_comment", v)
			}
			body, err := cl.Call(ctx, "files.completeUploadExternal", done)
			if err != nil {
				return err
			}
			if !a.JSON() && !a.Raw {
				successf(a, a.Out(c), "uploaded %s (%s)", filename, up.FileID)
				return nil
			}
			return a.Render(a.Out(c), body)
		},
	}
}

func conversationsFindCommand() *cli.Command {
	return &cli.Command{
		Name:      "find",
		Usage:     "Find channels by name (loads and filters the list)",
		ArgsUsage: "[query]",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "im", Usage: "Include direct messages"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			dir, err := a.Directory()
			if err != nil {
				return err
			}
			channels, err := dir.AllChannels(ctx)
			if err != nil {
				return err
			}
			query := strings.ToLower(strings.TrimPrefix(c.Args().First(), "#"))
			var rows []map[string]any
			for _, ch := range channels {
				if ch.IM && !c.Bool("im") {
					continue
				}
				if query != "" && !strings.Contains(strings.ToLower(ch.Name), query) {
					continue
				}
				rows = append(rows, map[string]any{
					"id": ch.ID, "name": ch.Name, "private": ch.Private, "im": ch.IM, "mpim": ch.MPIM,
				})
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i]["name"].(string) < rows[j]["name"].(string) })
			return renderRows(a, c, rows)
		},
	}
}

func usersFindCommand() *cli.Command {
	return &cli.Command{
		Name:      "find",
		Usage:     "Find users by name or email (loads and filters the list)",
		ArgsUsage: "[query]",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "include-bots", Usage: "Include bot users"},
			&cli.BoolFlag{Name: "include-deleted", Usage: "Include deactivated users"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			dir, err := a.Directory()
			if err != nil {
				return err
			}
			users, err := dir.AllUsers(ctx)
			if err != nil {
				return err
			}
			query := strings.ToLower(strings.TrimPrefix(c.Args().First(), "@"))
			var rows []map[string]any
			for _, u := range users {
				if u.Bot && !c.Bool("include-bots") {
					continue
				}
				if u.Deleted && !c.Bool("include-deleted") {
					continue
				}
				if query != "" && !strings.Contains(strings.ToLower(u.Name+" "+u.RealName+" "+u.DisplayName+" "+u.Email), query) {
					continue
				}
				rows = append(rows, map[string]any{
					"id": u.ID, "name": u.Name, "real_name": u.RealName, "email": u.Email,
				})
			}
			return renderRows(a, c, rows)
		},
	}
}

func conversationsReadCommand() *cli.Command {
	return &cli.Command{
		Name:      "read",
		Usage:     "Read recent messages with names resolved",
		ArgsUsage: "<channel>",
		Description: `Reads conversations.history and prints time, author, and text, with user
ids resolved to names. Pass --thread <ts> for a thread's replies.`,
		Flags: []cli.Flag{
			&cli.IntFlag{Name: "limit", Aliases: []string{"n"}, Value: 20, Usage: "Number of messages"},
			&cli.StringFlag{Name: "thread", Usage: "Read replies of this thread ts (conversations.replies)"},
			&cli.StringFlag{Name: "oldest", Usage: "Only messages after this ts"},
			&cli.StringFlag{Name: "latest", Usage: "Only messages before this ts"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			ch := c.Args().First()
			if ch == "" {
				return fmt.Errorf("expected <channel>")
			}
			id, err := resolveChannel(ctx, a, ch)
			if err != nil {
				return err
			}
			cl, err := a.Client()
			if err != nil {
				return err
			}
			method := "conversations.history"
			q := url.Values{"channel": {id}, "limit": {strconv.Itoa(int(c.Int("limit")))}}
			if t := c.String("thread"); t != "" {
				method, q = "conversations.replies", url.Values{"channel": {id}, "ts": {t}, "limit": {strconv.Itoa(int(c.Int("limit")))}}
			}
			for _, k := range []string{"oldest", "latest"} {
				if v := c.String(k); v != "" {
					q.Set(k, v)
				}
			}
			body, err := cl.Call(ctx, method, q)
			if err != nil {
				return err
			}
			if a.JSON() || a.Raw {
				return a.Render(a.Out(c), body)
			}
			page, _ := client.ParsePage(body)
			return renderMessages(ctx, a, c, page.Items)
		},
	}
}

func renderMessages(ctx context.Context, a *AppContext, c *cli.Command, items []json.RawMessage) error {
	dir, _ := a.Directory()
	// Oldest first.
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	var rows [][]string
	for _, raw := range items {
		var m struct {
			TS      string `json:"ts"`
			User    string `json:"user"`
			BotID   string `json:"bot_id"`
			Text    string `json:"text"`
			Subtype string `json:"subtype"`
			Replies int    `json:"reply_count"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		author := m.User
		if dir != nil && m.User != "" {
			author = dir.UserName(ctx, m.User)
		} else if m.User == "" && m.BotID != "" {
			author = "bot:" + m.BotID
		}
		text := output.Truncate(m.Text, 100)
		if m.Replies > 0 {
			text += fmt.Sprintf("  [%d replies]", m.Replies)
		}
		rows = append(rows, []string{formatTS(m.TS), m.TS, author, text})
	}
	return output.Table(a.Out(c), []string{"TIME", "TS", "FROM", "TEXT"}, rows)
}

func canvasesReadCommand() *cli.Command {
	return &cli.Command{
		Name:      "read",
		Usage:     "Read a canvas as Markdown",
		ArgsUsage: "<canvas-id>",
		Description: `Reads a canvas with canvases.getContent and prints its Markdown. A canvas
id looks like F0123456789.`,
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("expected <canvas-id>")
			}
			cl, err := a.Client()
			if err != nil {
				return err
			}
			body, err := cl.Call(ctx, "canvases.getContent", url.Values{"canvas_id": {id}})
			if err != nil {
				return err
			}
			if a.JSON() || a.Raw {
				return a.Render(a.Out(c), body)
			}
			var resp struct {
				Markdown string `json:"markdown"`
				Content  string `json:"content"`
			}
			_ = json.Unmarshal(body, &resp)
			text := resp.Markdown
			if text == "" {
				text = resp.Content
			}
			fmt.Fprintln(a.Out(c), text)
			return nil
		},
	}
}

func resolveChannel(ctx context.Context, a *AppContext, ref string) (string, error) {
	if a.NoResolve || client.IsChannelID(ref) {
		return ref, nil
	}
	dir, err := a.Directory()
	if err != nil {
		return "", err
	}
	return dir.ResolveChannel(ctx, ref)
}

func renderRows(a *AppContext, c *cli.Command, rows []map[string]any) error {
	w := a.Out(c)
	if a.JSON() || a.Raw {
		arr := make([]map[string]any, len(rows))
		copy(arr, rows)
		return output.WriteJSON(w, arr)
	}
	return output.Rows(w, rows, a.Columns)
}

func formatTS(ts string) string {
	sec, _, ok := strings.Cut(ts, ".")
	if !ok {
		sec = ts
	}
	n, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return ts
	}
	return time.Unix(n, 0).Format("01-02 15:04")
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
