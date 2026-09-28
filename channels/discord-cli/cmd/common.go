package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/channels/discord-cli/internal/client"
)

// clientFromCtx extracts the *client.Client from the cli context.
func clientFromCtx(c *cli.Context) (*client.Client, error) {
	cl, ok := c.App.Metadata["client"].(*client.Client)
	if !ok || cl == nil {
		return nil, fmt.Errorf("no Discord token configured — set DISCORD_BOT_TOKEN or run: discord-cli auth set <token>")
	}
	return cl, nil
}

// outputJSON checks the --json flag.
func outputJSON(c *cli.Context) bool {
	return c.Bool("json")
}

// printJSON writes v as indented JSON to w.
func printJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// printTable writes a simple text table to w.
func printTable(w io.Writer, headers []string, rows [][]string) {
	if len(headers) == 0 {
		return
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	for i, h := range headers {
		if i > 0 {
			fmt.Fprint(w, "  ")
		}
		fmt.Fprintf(w, "%-*s", widths[i], h)
	}
	fmt.Fprintln(w)
	for i, wi := range widths {
		if i > 0 {
			fmt.Fprint(w, "  ")
		}
		fmt.Fprint(w, strings.Repeat("-", wi))
	}
	fmt.Fprintln(w)
	for _, row := range rows {
		for i := 0; i < len(headers); i++ {
			if i > 0 {
				fmt.Fprint(w, "  ")
			}
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			fmt.Fprintf(w, "%-*s", widths[i], cell)
		}
		fmt.Fprintln(w)
	}
}

// truncate truncates s to max length, appending "..." if truncated.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

// encodeEmoji encodes an emoji for use in a Discord API URL path segment.
// Custom emoji format "name:id" is passed through; unicode emoji are URL-encoded.
func encodeEmoji(raw string) string {
	if strings.Contains(raw, ":") {
		return raw
	}
	return url.PathEscape(raw)
}

// resolveChannelID resolves a channel identifier to a channel ID.
// Accepts a numeric ID or a #channel-name (searches across all guilds).
func resolveChannelID(c *cli.Context, cl *client.Client, identifier string) (string, error) {
	name := strings.TrimPrefix(identifier, "#")
	allDigits := true
	for _, ch := range name {
		if ch < '0' || ch > '9' {
			allDigits = false
			break
		}
	}
	if allDigits && len(name) > 0 {
		return name, nil
	}
	var guilds []client.Guild
	if err := cl.DoJSON(c.Context, "GET", "/users/@me/guilds", nil, &guilds); err != nil {
		return "", fmt.Errorf("resolve channel %q: %w", identifier, err)
	}
	nameLower := strings.ToLower(name)
	for _, g := range guilds {
		var channels []client.Channel
		if err := cl.DoJSON(c.Context, "GET", "/guilds/"+g.ID+"/channels", nil, &channels); err != nil {
			continue
		}
		for _, ch := range channels {
			if strings.ToLower(ch.Name) == nameLower {
				return ch.ID, nil
			}
		}
	}
	return "", fmt.Errorf("channel %q not found", identifier)
}
