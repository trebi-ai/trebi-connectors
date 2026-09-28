package cmd

import (
	"fmt"
	"os"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/channels/discord-cli/internal/client"
)

// ChannelCommand returns the `channel` subcommand tree.
func ChannelCommand() *cli.Command {
	return &cli.Command{
		Name:  "channel",
		Usage: "List, create, delete, edit channels; trigger typing",
		Subcommands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List channels across all guilds (or one guild)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "server", Aliases: []string{"s"}, Usage: "guild ID to filter"},
				},
				Action: channelList,
			},
			{
				Name:      "create",
				Usage:     "Create a channel in a guild",
				ArgsUsage: "<guild_id> <name>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "type", Value: "text", Usage: "channel type: text, voice, category, forum"},
					&cli.StringFlag{Name: "topic"},
				},
				Action: channelCreate,
			},
			{
				Name:      "delete",
				Usage:     "Delete a channel",
				ArgsUsage: "<channel_id>",
				Action:    channelDelete,
			},
			{
				Name:      "info",
				Usage:     "Show channel details",
				ArgsUsage: "<channel_id>",
				Action:    channelInfo,
			},
			{
				Name:      "edit",
				Usage:     "Edit a channel",
				ArgsUsage: "<channel_id>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "name"},
					&cli.StringFlag{Name: "topic"},
					&cli.IntFlag{Name: "slowmode", Value: -1, Usage: "slowmode in seconds"},
					&cli.BoolFlag{Name: "nsfw"},
					&cli.BoolFlag{Name: "no-nsfw"},
				},
				Action: channelEdit,
			},
			{
				Name:      "typing",
				Usage:     "Trigger a typing indicator in a channel (~10s)",
				ArgsUsage: "<channel_id>",
				Action:    channelTyping,
			},
		},
	}
}

// channelTypeMap maps type string to Discord channel type int.
var channelTypeMap = map[string]int{
	"text":     0,
	"voice":    2,
	"category": 4,
	"forum":    15,
}

func channelList(c *cli.Context) error {
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}

	serverFilter := c.String("server")

	type guildChannels struct {
		GuildID   string           `json:"guild_id"`
		GuildName string           `json:"guild_name"`
		Channels  []client.Channel `json:"channels"`
	}

	var results []guildChannels

	if serverFilter != "" {
		var channels []client.Channel
		if err := cl.DoJSON(c.Context, "GET", "/guilds/"+serverFilter+"/channels", nil, &channels); err != nil {
			return err
		}
		results = append(results, guildChannels{GuildID: serverFilter, Channels: channels})
	} else {
		var guilds []client.Guild
		if err := cl.DoJSON(c.Context, "GET", "/users/@me/guilds", nil, &guilds); err != nil {
			return err
		}
		for _, g := range guilds {
			var channels []client.Channel
			if err := cl.DoJSON(c.Context, "GET", "/guilds/"+g.ID+"/channels", nil, &channels); err != nil {
				continue
			}
			results = append(results, guildChannels{GuildID: g.ID, GuildName: g.Name, Channels: channels})
		}
	}

	if outputJSON(c) {
		printJSON(os.Stdout, results)
	} else {
		headers := []string{"Server ID", "Server", "ID", "Name", "Type", "Topic"}
		var rows [][]string
		for _, gc := range results {
			for _, ch := range gc.Channels {
				rows = append(rows, []string{
					gc.GuildID,
					gc.GuildName,
					ch.ID,
					ch.Name,
					client.ChannelTypeName(ch.Type),
					truncate(ch.Topic, 40),
				})
			}
		}
		printTable(os.Stdout, headers, rows)
	}
	return nil
}

func channelCreate(c *cli.Context) error {
	if c.NArg() < 2 {
		return fmt.Errorf("usage: discord-cli channel create <guild_id> <name>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	guildID := c.Args().Get(0)
	name := c.Args().Get(1)
	typeName := c.String("type")

	typeInt, ok := channelTypeMap[typeName]
	if !ok {
		return fmt.Errorf("unknown channel type %q (use: text, voice, category, forum)", typeName)
	}

	body := map[string]any{
		"name": name,
		"type": typeInt,
	}
	if topic := c.String("topic"); topic != "" {
		body["topic"] = topic
	}

	var ch client.Channel
	if err := cl.DoJSON(c.Context, "POST", "/guilds/"+guildID+"/channels", body, &ch); err != nil {
		return err
	}
	if outputJSON(c) {
		printJSON(os.Stdout, ch)
	} else {
		fmt.Fprintf(os.Stdout, "Created channel %q (ID: %s, type: %s)\n", ch.Name, ch.ID, client.ChannelTypeName(ch.Type))
	}
	return nil
}

func channelDelete(c *cli.Context) error {
	if c.NArg() < 1 {
		return fmt.Errorf("usage: discord-cli channel delete <channel_id>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	channelID := c.Args().Get(0)
	if err := cl.DoNoBody(c.Context, "DELETE", "/channels/"+channelID); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Deleted channel", channelID)
	return nil
}

func channelInfo(c *cli.Context) error {
	if c.NArg() < 1 {
		return fmt.Errorf("usage: discord-cli channel info <channel_id>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	var ch client.Channel
	if err := cl.DoJSON(c.Context, "GET", "/channels/"+c.Args().Get(0), nil, &ch); err != nil {
		return err
	}
	if outputJSON(c) {
		printJSON(os.Stdout, ch)
	} else {
		fmt.Fprintf(os.Stdout, "ID:      %s\nName:    %s\nType:    %s\nGuild:   %s\nTopic:   %s\nNSFW:    %v\nParent:  %s\n",
			ch.ID, ch.Name, client.ChannelTypeName(ch.Type), ch.GuildID, ch.Topic, ch.NSFW, ch.ParentID)
	}
	return nil
}

func channelEdit(c *cli.Context) error {
	if c.NArg() < 1 {
		return fmt.Errorf("usage: discord-cli channel edit <channel_id> [flags]")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	channelID := c.Args().Get(0)

	body := map[string]any{}
	if c.IsSet("name") {
		body["name"] = c.String("name")
	}
	if c.IsSet("topic") {
		body["topic"] = c.String("topic")
	}
	if c.IsSet("slowmode") {
		body["rate_limit_per_user"] = c.Int("slowmode")
	}
	if c.IsSet("nsfw") {
		body["nsfw"] = true
	}
	if c.IsSet("no-nsfw") {
		body["nsfw"] = false
	}
	if len(body) == 0 {
		return fmt.Errorf("no edit flags provided")
	}

	var ch client.Channel
	if err := cl.DoJSON(c.Context, "PATCH", "/channels/"+channelID, body, &ch); err != nil {
		return err
	}
	if outputJSON(c) {
		printJSON(os.Stdout, ch)
	} else {
		fmt.Fprintf(os.Stdout, "Updated channel %q (ID: %s)\n", ch.Name, ch.ID)
	}
	return nil
}

func channelTyping(c *cli.Context) error {
	if c.NArg() < 1 {
		return fmt.Errorf("usage: discord-cli channel typing <channel_id>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	channelID := c.Args().Get(0)
	if err := cl.DoNoBody(c.Context, "POST", "/channels/"+channelID+"/typing"); err != nil {
		return err
	}
	if outputJSON(c) {
		printJSON(os.Stdout, map[string]any{
			"channel_id": channelID,
			"ok":         true,
		})
	} else {
		fmt.Fprintf(os.Stdout, "Typing in channel %s (expires ~10s)\n", channelID)
	}
	return nil
}
