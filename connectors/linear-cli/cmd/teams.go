package cmd

import (
	"github.com/urfave/cli/v2"
)

// TeamsCommand returns `teams list`.
func TeamsCommand() *cli.Command {
	return &cli.Command{
		Name:  "teams",
		Usage: "Work with teams",
		Subcommands: []*cli.Command{{
			Name:  "list",
			Usage: "List the teams that the key can see",
			Action: func(c *cli.Context) error {
				cl, err := clientFromCtx(c)
				if err != nil {
					return err
				}
				teams, err := cl.Teams(c.Context)
				if err != nil {
					return err
				}
				if c.Bool("json") {
					return printJSON(c.App.Writer, teams)
				}
				rows := make([][]string, len(teams))
				for i, t := range teams {
					rows[i] = []string{t.Key, t.Name, t.ID}
				}
				return printTable(c.App.Writer, []string{"KEY", "NAME", "ID"}, rows)
			},
		}},
	}
}

// IssuesCommand returns `issues list`.
func IssuesCommand() *cli.Command {
	return &cli.Command{
		Name:  "issues",
		Usage: "Work with issues",
		Subcommands: []*cli.Command{{
			Name:  "list",
			Usage: "List the issues that changed last",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "since", Usage: "only issues changed after this RFC 3339 time"},
				&cli.StringFlag{Name: "team", Usage: "only issues of this team key"},
				&cli.IntFlag{Name: "limit", Value: 25, Usage: "the maximum number of issues"},
			},
			Action: func(c *cli.Context) error {
				cl, err := clientFromCtx(c)
				if err != nil {
					return err
				}
				issues, err := cl.Issues(c.Context, c.String("since"), c.Int("limit"))
				if err != nil {
					return err
				}
				if team := c.String("team"); team != "" {
					kept := issues[:0]
					for _, is := range issues {
						if is.Team != nil && is.Team.Key == team {
							kept = append(kept, is)
						}
					}
					issues = kept
				}
				if c.Bool("json") {
					return printJSON(c.App.Writer, issues)
				}
				rows := make([][]string, len(issues))
				for i, is := range issues {
					state := ""
					if is.State != nil {
						state = is.State.Name
					}
					rows[i] = []string{is.Identifier, state, is.UpdatedAt, is.Title}
				}
				return printTable(c.App.Writer, []string{"ID", "STATE", "UPDATED", "TITLE"}, rows)
			},
		}},
	}
}
