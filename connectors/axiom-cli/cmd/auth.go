package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/internal/config"
	"github.com/urfave/cli/v2"
)

// AuthCommand returns the `auth` subcommand tree.
func AuthCommand() *cli.Command {
	return &cli.Command{
		Name:  "auth",
		Usage: "Manage Axiom API token (set, status, show, clear)",
		Subcommands: []*cli.Command{
			{
				Name:  "set",
				Usage: "Save an Axiom API token to ~/.cli-tools/axiom-cli/config.json (outside Trebi only)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "token", Usage: "API or personal access token", Required: true},
					&cli.StringFlag{Name: "org-id", Usage: "Org id (optional, but required for personal tokens)"},
					&cli.StringFlag{Name: "api-url", Usage: "Override API base URL"},
				},
				Action: authSet,
			},
			{
				Name:   "status",
				Usage:  "Show resolved token source and validate it against the API",
				Action: authStatus,
			},
			{
				Name:   "show",
				Usage:  "Print the contents of the config file with the token masked",
				Action: authShow,
			},
			{
				Name:   "clear",
				Usage:  "Delete the saved config file",
				Action: authClear,
			},
		},
	}
}

func authSet(c *cli.Context) error {
	if _, ok := config.Trebi(); ok {
		return config.ErrSetInTrebi
	}
	f := &config.File{
		Token:  c.String("token"),
		OrgID:  c.String("org-id"),
		APIURL: c.String("api-url"),
	}
	if err := config.Save(f); err != nil {
		return err
	}
	p, _ := config.Path()
	fmt.Fprintf(c.App.Writer, "wrote %s\n", p)
	return nil
}

func authStatus(c *cli.Context) error {
	r, err := resolved(c)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.App.Writer, "token-source: %s\n", r.Source)
	fmt.Fprintf(c.App.Writer, "token: %s\n", maskOrDash(r.Token))
	fmt.Fprintf(c.App.Writer, "org-id: %s\n", orDash(r.OrgID))
	fmt.Fprintf(c.App.Writer, "api-url: %s\n", r.APIURL)
	fmt.Fprintf(c.App.Writer, "config-file: %s\n", r.Path)

	if r.Token == "" {
		fmt.Fprintln(c.App.Writer, "validated: no-token")
		return cli.Exit("", 1)
	}

	cl := client.New(r.APIURL, r.Token, r.OrgID)
	raw, err := cl.Get(ctxOf(c), "/v2/datasets", nil)
	if err != nil {
		fmt.Fprintf(c.App.Writer, "validated: error (%v)\n", err)
		return cli.Exit("", 1)
	}
	var arr []json.RawMessage
	count := 0
	if json.Unmarshal(raw, &arr) == nil {
		count = len(arr)
	}
	fmt.Fprintf(c.App.Writer, "validated: ok (datasets: %d)\n", count)
	return nil
}

func authShow(c *cli.Context) error {
	if _, ok := config.Trebi(); ok {
		fmt.Fprintln(c.App.Writer, "Trebi sets the values: the connection inputs hold the token.")
		return nil
	}
	f, p, err := config.Load()
	if err != nil {
		return err
	}
	if f == nil {
		fmt.Fprintf(c.App.Writer, "path: %s\nstatus: not configured\n", p)
		return nil
	}
	masked := *f
	masked.Token = config.Mask(f.Token)
	b, _ := json.MarshalIndent(masked, "", "  ")
	fmt.Fprintf(c.App.Writer, "path: %s\n%s\n", p, string(b))
	return nil
}

func authClear(c *cli.Context) error {
	if _, ok := config.Trebi(); ok {
		return config.ErrSetInTrebi
	}
	if err := config.Clear(); err != nil {
		return err
	}
	p, _ := config.Path()
	fmt.Fprintf(c.App.Writer, "removed %s\n", p)
	return nil
}

func maskOrDash(token string) string {
	if token == "" {
		return "-"
	}
	return config.Mask(token)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
