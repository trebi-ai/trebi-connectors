package cmd

import (
	"fmt"
	"os"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/channels/discord-cli/internal/client"
)

// ReactionCommand returns the `reaction` subcommand tree.
func ReactionCommand() *cli.Command {
	return &cli.Command{
		Name:  "reaction",
		Usage: "Add, remove, list reactions",
		Subcommands: []*cli.Command{
			{
				Name:      "add",
				Usage:     "Add a reaction to a message",
				ArgsUsage: "<channel_id> <message_id> <emoji>",
				Action:    reactionAdd,
			},
			{
				Name:      "remove",
				Usage:     "Remove your reaction from a message",
				ArgsUsage: "<channel_id> <message_id> <emoji>",
				Action:    reactionRemove,
			},
			{
				Name:      "list",
				Usage:     "List reactions on a message",
				ArgsUsage: "<channel_id> <message_id>",
				Action:    reactionList,
			},
			{
				Name:      "users",
				Usage:     "List users who reacted with an emoji",
				ArgsUsage: "<channel_id> <message_id> <emoji>",
				Flags: []cli.Flag{
					&cli.IntFlag{Name: "limit", Value: 25},
				},
				Action: reactionUsers,
			},
		},
	}
}

func reactionAdd(c *cli.Context) error {
	if c.NArg() < 3 {
		return fmt.Errorf("usage: discord-cli reaction add <channel_id> <message_id> <emoji>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	emoji := encodeEmoji(c.Args().Get(2))
	path := fmt.Sprintf("/channels/%s/messages/%s/reactions/%s/@me", c.Args().Get(0), c.Args().Get(1), emoji)
	if err := cl.DoNoBody(c.Context, "PUT", path); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Reaction added")
	return nil
}

func reactionRemove(c *cli.Context) error {
	if c.NArg() < 3 {
		return fmt.Errorf("usage: discord-cli reaction remove <channel_id> <message_id> <emoji>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	emoji := encodeEmoji(c.Args().Get(2))
	path := fmt.Sprintf("/channels/%s/messages/%s/reactions/%s/@me", c.Args().Get(0), c.Args().Get(1), emoji)
	if err := cl.DoNoBody(c.Context, "DELETE", path); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Reaction removed")
	return nil
}

func reactionList(c *cli.Context) error {
	if c.NArg() < 2 {
		return fmt.Errorf("usage: discord-cli reaction list <channel_id> <message_id>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	var msg client.Message
	path := fmt.Sprintf("/channels/%s/messages/%s", c.Args().Get(0), c.Args().Get(1))
	if err := cl.DoJSON(c.Context, "GET", path, nil, &msg); err != nil {
		return err
	}

	if outputJSON(c) {
		printJSON(os.Stdout, msg.Reactions)
	} else {
		if len(msg.Reactions) == 0 {
			fmt.Fprintln(os.Stdout, "No reactions")
			return nil
		}
		for _, r := range msg.Reactions {
			name := r.Emoji.Name
			if r.Emoji.ID != "" {
				name = fmt.Sprintf("%s:%s", r.Emoji.Name, r.Emoji.ID)
			}
			fmt.Fprintf(os.Stdout, "%s  ×%d\n", name, r.Count)
		}
	}
	return nil
}

func reactionUsers(c *cli.Context) error {
	if c.NArg() < 3 {
		return fmt.Errorf("usage: discord-cli reaction users <channel_id> <message_id> <emoji>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	emoji := encodeEmoji(c.Args().Get(2))
	path := fmt.Sprintf("/channels/%s/messages/%s/reactions/%s?limit=%d",
		c.Args().Get(0), c.Args().Get(1), emoji, c.Int("limit"))

	var users []client.User
	if err := cl.DoJSON(c.Context, "GET", path, nil, &users); err != nil {
		return err
	}

	if outputJSON(c) {
		printJSON(os.Stdout, users)
	} else {
		for _, u := range users {
			fmt.Fprintf(os.Stdout, "%s (ID: %s)\n", u.Username, u.ID)
		}
	}
	return nil
}
