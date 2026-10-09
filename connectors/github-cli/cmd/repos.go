package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/client"
)

// ReposCommand returns `repos list`.
func ReposCommand() *cli.Command {
	return &cli.Command{
		Name:  "repos",
		Usage: "Repositories of the account",
		Subcommands: []*cli.Command{{
			Name:  "list",
			Usage: "List the repositories, newest push first",
			Flags: []cli.Flag{&cli.IntFlag{Name: "limit", Value: 30, Usage: "maximum number of repositories"}},
			Action: func(c *cli.Context) error {
				cl, err := clientFromCtx(c)
				if err != nil {
					return err
				}
				var all []client.Repo
				for page := 1; len(all) < c.Int("limit"); page++ {
					repos, more, err := cl.Repos(c.Context, page)
					if err != nil {
						return err
					}
					all = append(all, repos...)
					if !more {
						break
					}
				}
				all = all[:min(len(all), c.Int("limit"))]
				if c.Bool("json") {
					return printJSON(c.App.Writer, all)
				}
				rows := make([][]string, 0, len(all))
				for _, r := range all {
					rows = append(rows, []string{r.FullName, yes(r.Private), yes(r.Permissions.Admin)})
				}
				printTable(c.App.Writer, []string{"REPOSITORY", "PRIVATE", "ADMIN"}, rows)
				return nil
			},
		}},
	}
}

// EventsCommand returns `events list <owner/repo>`.
func EventsCommand() *cli.Command {
	return &cli.Command{
		Name:  "events",
		Usage: "Recent events of a repository",
		Subcommands: []*cli.Command{{
			Name:      "list",
			Usage:     "List the recent events of a repository",
			ArgsUsage: "<owner/repo>",
			Action: func(c *cli.Context) error {
				repo, err := repoArg(c)
				if err != nil {
					return err
				}
				cl, err := clientFromCtx(c)
				if err != nil {
					return err
				}
				page, err := cl.Events(c.Context, repo, "")
				if err != nil {
					return err
				}
				if c.Bool("json") {
					return printJSON(c.App.Writer, page.Events)
				}
				rows := make([][]string, 0, len(page.Events))
				for _, e := range page.Events {
					rows = append(rows, []string{e.ID, e.Type, e.Actor.Login, e.CreatedAt})
				}
				printTable(c.App.Writer, []string{"ID", "TYPE", "ACTOR", "CREATED"}, rows)
				return nil
			},
		}},
	}
}

// HooksCommand returns `hooks list <owner/repo>`.
func HooksCommand() *cli.Command {
	return &cli.Command{
		Name:  "hooks",
		Usage: "Webhooks of a repository",
		Subcommands: []*cli.Command{{
			Name:      "list",
			Usage:     "List the webhooks of a repository (needs admin access)",
			ArgsUsage: "<owner/repo>",
			Action: func(c *cli.Context) error {
				repo, err := repoArg(c)
				if err != nil {
					return err
				}
				cl, err := clientFromCtx(c)
				if err != nil {
					return err
				}
				hooks, err := cl.Hooks(c.Context, repo)
				if err != nil {
					return err
				}
				if c.Bool("json") {
					return printJSON(c.App.Writer, hooks)
				}
				rows := make([][]string, 0, len(hooks))
				for _, h := range hooks {
					rows = append(rows, []string{strconv.FormatInt(h.ID, 10), h.Config.URL, yes(h.Active), strings.Join(h.Events, ",")})
				}
				printTable(c.App.Writer, []string{"ID", "URL", "ACTIVE", "EVENTS"}, rows)
				return nil
			},
		}},
	}
}

func repoArg(c *cli.Context) (string, error) {
	repo := c.Args().First()
	if owner, name, ok := strings.Cut(repo, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("give the repository as owner/repo, not %q", repo)
	}
	return repo, nil
}

func yes(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
