// Package cmd holds the linear-cli commands.
package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/linear-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/linear-cli/internal/config"
)

// settingsFromCtx returns what config.Resolve found in the before hook.
func settingsFromCtx(c *cli.Context) config.Settings {
	s, _ := c.App.Metadata["settings"].(config.Settings) //nolint:errcheck // the zero value is no key
	return s
}

// clientFromCtx returns a client with the resolved key.
func clientFromCtx(c *cli.Context) (*client.Client, error) {
	s := settingsFromCtx(c)
	switch {
	case s.Key != "":
		return client.New(s.Key), nil
	case s.Trebi:
		return nil, fmt.Errorf("%s is not set in Trebi", config.KeyLabel)
	}
	return nil, fmt.Errorf("no Linear API key: set %s, use --key, or write %s", config.EnvKey, config.Path())
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printTable(w io.Writer, headers []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	return tw.Flush()
}
