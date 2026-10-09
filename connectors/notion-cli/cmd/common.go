package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/notion-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/notion-cli/internal/config"
)

// settingsFromCtx returns what config.Resolve found in the before hook.
func settingsFromCtx(c *cli.Context) config.Settings {
	s, _ := c.App.Metadata["settings"].(config.Settings) //nolint:errcheck // the zero value is no token
	return s
}

// clientFromCtx returns the client of the before hook.
func clientFromCtx(c *cli.Context) (*client.Client, error) {
	if cl, ok := c.App.Metadata["client"].(*client.Client); ok && cl != nil {
		return cl, nil
	}
	if settingsFromCtx(c).Trebi {
		return nil, fmt.Errorf("%s is not set in Trebi", config.TokenLabel)
	}
	return nil, errors.New("no Notion secret: set NOTION_TOKEN or run: notion-cli auth set <secret>")
}

// printJSON writes v as indented JSON to w.
func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// printTable writes a simple text table to w.
func printTable(w io.Writer, headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) {
				widths[i] = max(widths[i], len(cell))
			}
		}
	}
	line := func(cells []string) {
		parts := make([]string, len(headers))
		for i := range headers {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			parts[i] = fmt.Sprintf("%-*s", widths[i], cell)
		}
		fmt.Fprintln(w, strings.TrimRight(strings.Join(parts, "  "), " "))
	}
	line(headers)
	for _, row := range rows {
		line(row)
	}
}
