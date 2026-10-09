// Package cmd holds the commands of github-cli.
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/config"
)

func settingsFromCtx(c *cli.Context) config.Settings {
	s, _ := c.App.Metadata["settings"].(config.Settings) //nolint:errcheck // the zero value is no token
	return s
}

// clientFromCtx returns the client, or an error when no token is set.
func clientFromCtx(c *cli.Context) (*client.Client, error) {
	cl, _ := c.App.Metadata["client"].(*client.Client) //nolint:errcheck // nil gives the error below
	if cl != nil && cl.Token != "" {
		return cl, nil
	}
	if settingsFromCtx(c).Trebi {
		return nil, errors.New("log in to GitHub in Trebi first")
	}
	return nil, errNoToken
}

var errNoToken = errors.New("no GitHub token: set GITHUB_TOKEN or use --token")

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
			widths[i] = max(widths[i], len(cell))
		}
	}
	line := func(cells []string) {
		parts := make([]string, len(cells))
		for i, cell := range cells {
			parts[i] = fmt.Sprintf("%-*s", widths[i], cell)
		}
		fmt.Fprintln(w, strings.TrimRight(strings.Join(parts, "  "), " "))
	}
	line(headers)
	dashes := make([]string, len(headers))
	for i, wi := range widths {
		dashes[i] = strings.Repeat("-", wi)
	}
	line(dashes)
	for _, row := range rows {
		line(row)
	}
}
