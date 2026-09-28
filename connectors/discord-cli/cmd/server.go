package cmd

import (
	"fmt"
	"os"
	"strconv"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/discord-cli/internal/client"
)

// ServerCommand returns the `server` subcommand tree.
func ServerCommand() *cli.Command {
	return &cli.Command{
		Name:    "server",
		Aliases: []string{"guild"},
		Usage:   "List and inspect servers (guilds)",
		Subcommands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List servers the bot is in",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "counts", Usage: "include approximate member and presence counts"},
				},
				Action: serverList,
			},
			{
				Name:      "info",
				Usage:     "Show server details",
				ArgsUsage: "<server_id>",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "counts", Usage: "include approximate member and presence counts"},
				},
				Action: serverInfo,
			},
		},
	}
}

func serverList(c *cli.Context) error {
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}

	path := "/users/@me/guilds"
	if c.Bool("counts") {
		path += "?with_counts=true"
	}

	var guilds []client.Guild
	if err := cl.DoJSON(c.Context, "GET", path, nil, &guilds); err != nil {
		return err
	}

	if outputJSON(c) {
		printJSON(os.Stdout, guilds)
	} else {
		headers := []string{"ID", "Name", "Owner"}
		if c.Bool("counts") {
			headers = append(headers, "Members", "Online")
		}
		var rows [][]string
		for _, g := range guilds {
			row := []string{g.ID, g.Name, fmt.Sprintf("%v", g.Owner)}
			if c.Bool("counts") {
				row = append(row, strconv.Itoa(g.ApproximateMemberCount), strconv.Itoa(g.ApproximatePresenceCount))
			}
			rows = append(rows, row)
		}
		printTable(os.Stdout, headers, rows)
	}
	return nil
}

func serverInfo(c *cli.Context) error {
	if c.NArg() < 1 {
		return fmt.Errorf("usage: discord-cli server info <server_id>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}

	serverID := c.Args().Get(0)
	path := "/guilds/" + serverID
	if c.Bool("counts") {
		path += "?with_counts=true"
	}

	var g client.Guild
	if err := cl.DoJSON(c.Context, "GET", path, nil, &g); err != nil {
		return err
	}

	if outputJSON(c) {
		printJSON(os.Stdout, g)
	} else {
		fmt.Fprintf(os.Stdout, "ID:       %s\nName:     %s\nOwner ID: %s\n", g.ID, g.Name, g.OwnerID)
		if g.ApproximateMemberCount > 0 {
			fmt.Fprintf(os.Stdout, "Members:  ~%d\nOnline:   ~%d\n", g.ApproximateMemberCount, g.ApproximatePresenceCount)
		}
		if g.MemberCount > 0 {
			fmt.Fprintf(os.Stdout, "Members:  %d\n", g.MemberCount)
		}
	}
	return nil
}
