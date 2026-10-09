package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/config"
)

// settingsFromCtx returns what config.Resolve found in the before hook.
func settingsFromCtx(c *cli.Context) config.Settings {
	s, _ := c.App.Metadata["settings"].(config.Settings) //nolint:errcheck // the zero value has no login
	return s
}

// clientFromCtx builds a client on the saved login. In Trebi mode only
// serve writes auth.json, so a refreshed token stays in memory.
func clientFromCtx(c *cli.Context) (*client.Client, error) {
	s := settingsFromCtx(c)
	sess, err := client.LoadSession(s.AuthPath())
	if err != nil {
		return nil, err
	}
	if sess.AccessToken == "" {
		if s.Trebi {
			return nil, errors.New("not logged in: log in to Microsoft in Trebi")
		}
		return nil, errors.New("not logged in: run mstodo-cli auth login")
	}
	cl := client.New(s.ClientID, sess)
	if !s.Trebi {
		cl.OnRefresh = func(sess client.Session) error { return sess.Save(s.AuthPath()) }
	}
	return cl, nil
}

// outputJSON checks the --json flag.
func outputJSON(c *cli.Context) bool {
	return c.Bool("json")
}

// printJSON writes v as indented JSON to w.
func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// printTable writes a simple text table to w.
func printTable(w io.Writer, headers []string, rows [][]string) error {
	var b strings.Builder
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
	line := func(cells []string) {
		for i := range headers {
			if i > 0 {
				b.WriteString("  ")
			}
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			fmt.Fprintf(&b, "%-*s", widths[i], cell)
		}
		b.WriteString("\n")
	}
	line(headers)
	dashes := make([]string, len(widths))
	for i, wi := range widths {
		dashes[i] = strings.Repeat("-", wi)
	}
	line(dashes)
	for _, row := range rows {
		line(row)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
