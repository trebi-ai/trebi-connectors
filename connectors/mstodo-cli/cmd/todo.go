package cmd

import (
	"github.com/urfave/cli/v2"
)

// ListsCommand returns the `lists` command.
func ListsCommand() *cli.Command {
	return &cli.Command{
		Name:  "lists",
		Usage: "Work with To Do lists",
		Subcommands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List the To Do lists",
				Action: func(c *cli.Context) error {
					cl, err := clientFromCtx(c)
					if err != nil {
						return err
					}
					lists, err := cl.Lists(c.Context)
					if err != nil {
						return err
					}
					if outputJSON(c) {
						return printJSON(c.App.Writer, lists)
					}
					rows := make([][]string, 0, len(lists))
					for _, l := range lists {
						rows = append(rows, []string{l.ID, l.DisplayName})
					}
					return printTable(c.App.Writer, []string{"ID", "NAME"}, rows)
				},
			},
		},
	}
}

// TasksCommand returns the `tasks` command.
func TasksCommand() *cli.Command {
	return &cli.Command{
		Name:  "tasks",
		Usage: "Work with To Do tasks",
		Subcommands: []*cli.Command{
			{
				Name:      "list",
				Usage:     "List the tasks of a list",
				ArgsUsage: "<list id>",
				Action: func(c *cli.Context) error {
					if c.NArg() != 1 {
						return cli.Exit("usage: mstodo-cli tasks list <list id>", 2)
					}
					cl, err := clientFromCtx(c)
					if err != nil {
						return err
					}
					tasks, err := cl.Tasks(c.Context, c.Args().First())
					if err != nil {
						return err
					}
					if outputJSON(c) {
						return printJSON(c.App.Writer, tasks)
					}
					rows := make([][]string, 0, len(tasks))
					for _, t := range tasks {
						due := ""
						if t.DueDateTime != nil {
							due = t.DueDateTime.DateTime
						}
						rows = append(rows, []string{t.ID, t.Status, due, t.Title})
					}
					return printTable(c.App.Writer, []string{"ID", "STATUS", "DUE", "TITLE"}, rows)
				},
			},
		},
	}
}
