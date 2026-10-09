package cmd

import (
	"os"

	"github.com/urfave/cli/v2"
)

// PagesCommand returns the `pages` subcommand tree.
func PagesCommand() *cli.Command {
	return &cli.Command{
		Name:  "pages",
		Usage: "Read pages that are shared with the integration",
		Subcommands: []*cli.Command{
			{
				Name:  "search",
				Usage: "Find pages, newest edit first",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "query", Aliases: []string{"q"}, Usage: "text in the page title"},
					&cli.IntFlag{Name: "limit", Value: 20, Usage: "maximum number of pages (1-100)"},
				},
				Action: pagesSearch,
			},
		},
	}
}

func pagesSearch(c *cli.Context) error {
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	res, err := cl.EditedPages(c.Context, c.String("query"), "", min(max(c.Int("limit"), 1), 100))
	if err != nil {
		return err
	}
	if c.Bool("json") {
		return printJSON(os.Stdout, res.Results)
	}
	rows := make([][]string, 0, len(res.Results))
	for _, p := range res.Results {
		rows = append(rows, []string{p.ID, p.LastEditedTime, p.Title()})
	}
	printTable(os.Stdout, []string{"ID", "EDITED", "TITLE"}, rows)
	return nil
}
