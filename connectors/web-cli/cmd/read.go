// Package cmd has the commands of web-cli.
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/web-cli/internal/watch"
)

// ReadCommand returns the `read` command: it reads one address once.
func ReadCommand(version string) *cli.Command {
	return &cli.Command{
		Name:      "read",
		Usage:     "Read one address: the entries of a feed, or the text of a page",
		ArgsUsage: "<url>",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "selector", Aliases: []string{"s"}, Usage: "CSS selector of the part of the page to read"},
			&cli.BoolFlag{Name: "json", Aliases: []string{"j"}, Usage: "output as JSON"},
		},
		Action: func(c *cli.Context) error {
			if c.NArg() != 1 {
				return errors.New("give one address")
			}
			res, err := watch.NewFetcher(nil, version).Read(c.Context, c.Args().First(), c.String("selector"))
			if err != nil {
				return err
			}
			w := c.App.Writer
			if c.Bool("json") {
				enc := json.NewEncoder(w)
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			}
			if f := res.Feed; f != nil {
				fmt.Fprintf(w, "%s (%d entries)\n", f.Title, len(f.Entries))
				for _, e := range f.Entries {
					fmt.Fprintf(w, "- %s  %s\n  %s\n", e.Published, e.Title, e.Link)
				}
				return nil
			}
			fmt.Fprintf(w, "%s (%d matches)\n\n%s\n", res.Page.Title, res.Page.Matches, res.Page.Text)
			return nil
		},
	}
}
