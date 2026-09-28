package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/src/discord-cli/internal/client"
)

// MessageCommand returns the `message` subcommand tree.
func MessageCommand() *cli.Command {
	return &cli.Command{
		Name:  "message",
		Usage: "Send, list, edit, delete messages",
		Subcommands: []*cli.Command{
			{
				Name:      "send",
				Usage:     "Send a message to a channel",
				ArgsUsage: "<channel_id> <text>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "embed-title"},
					&cli.StringFlag{Name: "embed-desc"},
					&cli.IntFlag{Name: "embed-color"},
					&cli.StringFlag{Name: "embed-footer"},
					&cli.StringFlag{Name: "embed-image"},
					&cli.StringFlag{Name: "embed-thumbnail"},
					&cli.StringFlag{Name: "embed-author"},
					&cli.StringSliceFlag{Name: "embed-field", Usage: "name=value (repeatable)"},
					&cli.StringSliceFlag{Name: "file", Aliases: []string{"f"}, Usage: "file path to attach (repeatable)"},
				},
				Action: messageSend,
			},
			{
				Name:      "list",
				Usage:     "List recent messages in a channel",
				ArgsUsage: "<channel_id>",
				Flags: []cli.Flag{
					&cli.IntFlag{Name: "limit", Value: 25},
					&cli.StringFlag{Name: "before", Usage: "message ID, date (2025-01-15), or timestamp (2025-01-15T00:00:00Z)"},
					&cli.StringFlag{Name: "after", Usage: "message ID, date (2025-01-15), or timestamp (2025-01-15T00:00:00Z)"},
				},
				Action: messageList,
			},
			{
				Name:      "get",
				Usage:     "Get a single message",
				ArgsUsage: "<channel_id> <message_id>",
				Action:    messageGet,
			},
			{
				Name:      "edit",
				Usage:     "Edit a message",
				ArgsUsage: "<channel_id> <message_id> <new_text>",
				Action:    messageEdit,
			},
			{
				Name:      "delete",
				Usage:     "Delete a message",
				ArgsUsage: "<channel_id> <message_id>",
				Action:    messageDelete,
			},
			{
				Name:      "reply",
				Usage:     "Reply to a message",
				ArgsUsage: "<channel_id> <message_id> <text>",
				Flags: []cli.Flag{
					&cli.StringSliceFlag{Name: "file", Aliases: []string{"f"}, Usage: "file path to attach"},
				},
				Action: messageReply,
			},
			{
				Name:  "search",
				Usage: "Search messages by content including embeds (client-side filter)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "channel", Aliases: []string{"c"}, Required: true, Usage: "channel ID or #name"},
					&cli.StringFlag{Name: "query", Aliases: []string{"q"}, Required: true, Usage: "search text"},
					&cli.IntFlag{Name: "limit", Aliases: []string{"n"}, Value: 0, Usage: "max results to return (0 = all matches)"},
					&cli.IntFlag{Name: "scan", Value: 100, Usage: "messages to scan from history (paginates if > 100)"},
					&cli.StringFlag{Name: "author", Usage: "filter by author username"},
					&cli.StringFlag{Name: "before", Usage: "message ID, date (2025-01-15), or timestamp (2025-01-15T00:00:00Z)"},
					&cli.StringFlag{Name: "after", Usage: "message ID, date (2025-01-15), or timestamp (2025-01-15T00:00:00Z)"},
				},
				Action: messageSearch,
			},
			{
				Name:      "bulk-delete",
				Usage:     "Bulk delete messages (2-100, < 14 days old)",
				ArgsUsage: "<channel_id> <message_id> [message_id...]",
				Action:    messageBulkDelete,
			},
		},
	}
}

func messageSend(c *cli.Context) error {
	if c.NArg() < 2 {
		return fmt.Errorf("usage: discord-cli message send <channel_id> <text>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	channelID := c.Args().Get(0)
	text := c.Args().Get(1)

	files := c.StringSlice("file")
	if len(files) > 0 {
		return messageSendWithFiles(c, cl, channelID, text, files)
	}

	body := map[string]any{"content": text}
	embed := buildEmbed(c)
	if embed != nil {
		body["embeds"] = []any{embed}
	}

	var msg client.Message
	if err := cl.DoJSON(c.Context, "POST", "/channels/"+channelID+"/messages", body, &msg); err != nil {
		return err
	}
	printMessageOutput(c, &msg)
	return nil
}

func messageSendWithFiles(c *cli.Context, cl *client.Client, channelID, text string, filePaths []string) error {
	payload := map[string]any{"content": text}
	embed := buildEmbed(c)
	if embed != nil {
		payload["embeds"] = []any{embed}
	}
	payloadJSON, _ := json.Marshal(payload)

	var uploads []client.FileUpload
	for _, fp := range filePaths {
		uploads = append(uploads, client.FileUpload{
			Filename: fp[strings.LastIndex(fp, "/")+1:],
			Path:     fp,
		})
	}

	msg, err := cl.Upload(c.Context, "/channels/"+channelID+"/messages",
		map[string]string{"payload_json": string(payloadJSON)}, uploads)
	if err != nil {
		return err
	}
	printMessageOutput(c, msg)
	return nil
}

func messageList(c *cli.Context) error {
	if c.NArg() < 1 {
		return fmt.Errorf("usage: discord-cli message list <channel_id>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	channelID := c.Args().Get(0)
	path := fmt.Sprintf("/channels/%s/messages?limit=%d", channelID, c.Int("limit"))
	if b := c.String("before"); b != "" {
		sf, err := resolveSnowflake(b)
		if err != nil {
			return fmt.Errorf("--before: %w", err)
		}
		path += "&before=" + sf
	}
	if a := c.String("after"); a != "" {
		sf, err := resolveSnowflake(a)
		if err != nil {
			return fmt.Errorf("--after: %w", err)
		}
		path += "&after=" + sf
	}

	var msgs []client.Message
	if err := cl.DoJSON(c.Context, "GET", path, nil, &msgs); err != nil {
		return err
	}

	if outputJSON(c) {
		printJSON(os.Stdout, msgs)
	} else {
		for i := len(msgs) - 1; i >= 0; i-- {
			m := msgs[i]
			author := ""
			if m.Author != nil {
				author = m.Author.Username
			}
			fmt.Fprintf(os.Stdout, "[%s] %s: %s  (ID: %s)\n",
				m.Timestamp.Format(time.RFC3339), author, truncate(m.Content, 120), m.ID)
		}
	}
	return nil
}

func messageGet(c *cli.Context) error {
	if c.NArg() < 2 {
		return fmt.Errorf("usage: discord-cli message get <channel_id> <message_id>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	var msg client.Message
	path := fmt.Sprintf("/channels/%s/messages/%s", c.Args().Get(0), c.Args().Get(1))
	if err := cl.DoJSON(c.Context, "GET", path, nil, &msg); err != nil {
		return err
	}
	printMessageOutput(c, &msg)
	return nil
}

func messageEdit(c *cli.Context) error {
	if c.NArg() < 3 {
		return fmt.Errorf("usage: discord-cli message edit <channel_id> <message_id> <new_text>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/channels/%s/messages/%s", c.Args().Get(0), c.Args().Get(1))
	body := map[string]any{"content": c.Args().Get(2)}
	var msg client.Message
	if err := cl.DoJSON(c.Context, "PATCH", path, body, &msg); err != nil {
		return err
	}
	printMessageOutput(c, &msg)
	return nil
}

func messageDelete(c *cli.Context) error {
	if c.NArg() < 2 {
		return fmt.Errorf("usage: discord-cli message delete <channel_id> <message_id>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/channels/%s/messages/%s", c.Args().Get(0), c.Args().Get(1))
	if err := cl.DoNoBody(c.Context, "DELETE", path); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Deleted message", c.Args().Get(1))
	return nil
}

func messageReply(c *cli.Context) error {
	if c.NArg() < 3 {
		return fmt.Errorf("usage: discord-cli message reply <channel_id> <message_id> <text>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	channelID := c.Args().Get(0)
	messageID := c.Args().Get(1)
	text := c.Args().Get(2)

	files := c.StringSlice("file")
	if len(files) > 0 {
		payload := map[string]any{
			"content":           text,
			"message_reference": map[string]string{"message_id": messageID},
		}
		payloadJSON, _ := json.Marshal(payload)
		var uploads []client.FileUpload
		for _, fp := range files {
			uploads = append(uploads, client.FileUpload{
				Filename: fp[strings.LastIndex(fp, "/")+1:],
				Path:     fp,
			})
		}
		msg, err := cl.Upload(c.Context, "/channels/"+channelID+"/messages",
			map[string]string{"payload_json": string(payloadJSON)}, uploads)
		if err != nil {
			return err
		}
		printMessageOutput(c, msg)
		return nil
	}

	body := map[string]any{
		"content":           text,
		"message_reference": map[string]string{"message_id": messageID},
	}
	var msg client.Message
	if err := cl.DoJSON(c.Context, "POST", "/channels/"+channelID+"/messages", body, &msg); err != nil {
		return err
	}
	printMessageOutput(c, &msg)
	return nil
}

func messageSearch(c *cli.Context) error {
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}

	channelID, err := resolveChannelID(c, cl, c.String("channel"))
	if err != nil {
		return err
	}
	query := strings.ToLower(c.String("query"))
	scan := c.Int("scan")
	maxResults := c.Int("limit")

	var beforeSF, afterSF string
	if b := c.String("before"); b != "" {
		beforeSF, err = resolveSnowflake(b)
		if err != nil {
			return fmt.Errorf("--before: %w", err)
		}
	}
	if a := c.String("after"); a != "" {
		afterSF, err = resolveSnowflake(a)
		if err != nil {
			return fmt.Errorf("--after: %w", err)
		}
	}

	authorFilter := strings.ToLower(c.String("author"))
	var matched []client.Message
	scanned := 0
	cursor := beforeSF

	for scanned < scan {
		batch := scan - scanned
		if batch > 100 {
			batch = 100
		}
		path := fmt.Sprintf("/channels/%s/messages?limit=%d", channelID, batch)
		if cursor != "" {
			path += "&before=" + cursor
		}
		if afterSF != "" {
			path += "&after=" + afterSF
		}

		var msgs []client.Message
		if err := cl.DoJSON(c.Context, "GET", path, nil, &msgs); err != nil {
			return err
		}
		if len(msgs) == 0 {
			break
		}
		scanned += len(msgs)

		for _, m := range msgs {
			if authorFilter != "" && m.Author != nil && strings.ToLower(m.Author.Username) != authorFilter {
				continue
			}
			if !messageMatchesQuery(m, query) {
				continue
			}
			matched = append(matched, m)
			if maxResults > 0 && len(matched) >= maxResults {
				break
			}
		}
		if maxResults > 0 && len(matched) >= maxResults {
			break
		}

		cursor = msgs[len(msgs)-1].ID
		if len(msgs) < batch {
			break
		}
	}

	if outputJSON(c) {
		printJSON(os.Stdout, matched)
	} else {
		fmt.Fprintf(os.Stdout, "Found %d matching messages (scanned %d)\n", len(matched), scanned)
		for _, m := range matched {
			author := ""
			if m.Author != nil {
				author = m.Author.Username
			}
			fmt.Fprintf(os.Stdout, "[%s] %s: %s  (ID: %s)\n",
				m.Timestamp.Format(time.RFC3339), author, truncate(m.Content, 120), m.ID)
		}
	}
	return nil
}

// messageMatchesQuery checks if a message's content or embeds contain the query.
func messageMatchesQuery(m client.Message, query string) bool {
	if strings.Contains(strings.ToLower(m.Content), query) {
		return true
	}
	for _, e := range m.Embeds {
		if strings.Contains(strings.ToLower(e.Title), query) ||
			strings.Contains(strings.ToLower(e.Description), query) {
			return true
		}
		if e.Footer != nil && strings.Contains(strings.ToLower(e.Footer.Text), query) {
			return true
		}
		if e.Author != nil && strings.Contains(strings.ToLower(e.Author.Name), query) {
			return true
		}
		for _, f := range e.Fields {
			if strings.Contains(strings.ToLower(f.Name), query) ||
				strings.Contains(strings.ToLower(f.Value), query) {
				return true
			}
		}
	}
	return false
}

// discordEpochMs is the Discord epoch (2015-01-01T00:00:00Z) in milliseconds.
const discordEpochMs = 1420070400000

// resolveSnowflake interprets s as a message ID (all digits), an RFC3339
// timestamp, or a date (YYYY-MM-DD) and returns a Discord snowflake string.
func resolveSnowflake(s string) (string, error) {
	allDigits := true
	for _, c := range s {
		if c < '0' || c > '9' {
			allDigits = false
			break
		}
	}
	if allDigits && len(s) > 0 {
		return s, nil
	}

	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse("2006-01-02", s)
	}
	if err != nil {
		return "", fmt.Errorf("expected message ID, RFC3339 (2025-01-15T00:00:00Z), or date (2025-01-15), got %q", s)
	}
	return snowflakeFromTime(t), nil
}

// snowflakeFromTime converts a time.Time to a Discord snowflake string.
func snowflakeFromTime(t time.Time) string {
	ms := t.UnixMilli() - discordEpochMs
	return fmt.Sprintf("%d", ms<<22)
}

func messageBulkDelete(c *cli.Context) error {
	if c.NArg() < 2 {
		return fmt.Errorf("usage: discord-cli message bulk-delete <channel_id> <msg_id> [msg_id...]")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	channelID := c.Args().Get(0)
	ids := c.Args().Slice()[1:]

	body := map[string]any{"messages": ids}
	path := fmt.Sprintf("/channels/%s/messages/bulk-delete", channelID)
	if err := cl.DoJSON(c.Context, "POST", path, body, nil); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Bulk deleted %d messages\n", len(ids))
	return nil
}

// buildEmbed creates an embed map from CLI flags. Returns nil if no embed flags set.
func buildEmbed(c *cli.Context) map[string]any {
	title := c.String("embed-title")
	desc := c.String("embed-desc")
	if title == "" && desc == "" {
		return nil
	}
	embed := map[string]any{}
	if title != "" {
		embed["title"] = title
	}
	if desc != "" {
		embed["description"] = desc
	}
	if c.Int("embed-color") != 0 {
		embed["color"] = c.Int("embed-color")
	}
	if f := c.String("embed-footer"); f != "" {
		embed["footer"] = map[string]string{"text": f}
	}
	if img := c.String("embed-image"); img != "" {
		embed["image"] = map[string]string{"url": img}
	}
	if th := c.String("embed-thumbnail"); th != "" {
		embed["thumbnail"] = map[string]string{"url": th}
	}
	if a := c.String("embed-author"); a != "" {
		embed["author"] = map[string]string{"name": a}
	}
	if fields := c.StringSlice("embed-field"); len(fields) > 0 {
		var ef []map[string]any
		for _, f := range fields {
			name, value, ok := strings.Cut(f, "=")
			if !ok {
				continue
			}
			ef = append(ef, map[string]any{"name": name, "value": value, "inline": false})
		}
		embed["fields"] = ef
	}
	return embed
}

func printMessageOutput(c *cli.Context, msg *client.Message) {
	if outputJSON(c) {
		printJSON(os.Stdout, msg)
	} else {
		author := ""
		if msg.Author != nil {
			author = msg.Author.Username
		}
		fmt.Fprintf(os.Stdout, "Message %s by %s: %s\n", msg.ID, author, truncate(msg.Content, 120))
	}
}
