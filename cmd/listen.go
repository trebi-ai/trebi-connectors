package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/flarco/cli-tools/discord-cli/internal/gateway"
)

// ListenCommand returns the `listen` command for streaming gateway events.
func ListenCommand() *cli.Command {
	return &cli.Command{
		Name:  "listen",
		Usage: "Listen to real-time Discord events via Gateway",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "server", Aliases: []string{"s"}, Usage: "guild ID to filter"},
			&cli.StringFlag{Name: "channel", Aliases: []string{"c"}, Usage: "channel ID to filter"},
			&cli.StringFlag{Name: "events", Aliases: []string{"e"}, Usage: "comma-separated event categories: messages,reactions,members,voice (default: all)"},
			&cli.BoolFlag{Name: "include-bots", Usage: "include bot messages"},
		},
		Action: listenAction,
	}
}

// intentBits maps event categories to Discord Gateway intent bits.
// messages includes GUILD_MESSAGES + DIRECT_MESSAGES + MESSAGE_CONTENT so
// listen --events messages receives both guild and DM MESSAGE_* events.
var intentBits = map[string]int{
	"guilds":    1 << 0,
	"members":   1 << 1,
	"voice":     1 << 7,
	"messages":  (1 << 9) | (1 << 12) | (1 << 15), // guild + DM + content
	"reactions": (1 << 10) | (1 << 13),             // guild + DM reactions
}

// eventCategoryMap maps Discord event types to categories for filtering.
var eventCategoryMap = map[string]string{
	"MESSAGE_CREATE":          "messages",
	"MESSAGE_UPDATE":          "messages",
	"MESSAGE_DELETE":          "messages",
	"MESSAGE_REACTION_ADD":    "reactions",
	"MESSAGE_REACTION_REMOVE": "reactions",
	"GUILD_MEMBER_ADD":        "members",
	"GUILD_MEMBER_REMOVE":     "members",
	"VOICE_STATE_UPDATE":      "voice",
}

func listenAction(c *cli.Context) error {
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}

	eventsFlag := c.String("events")
	allEvents := eventsFlag == "" || eventsFlag == "all"

	var categories []string
	if !allEvents {
		categories = strings.Split(eventsFlag, ",")
		for i := range categories {
			categories[i] = strings.TrimSpace(categories[i])
		}
	}

	intents := intentBits["guilds"]
	if allEvents {
		for _, bits := range intentBits {
			intents |= bits
		}
	} else {
		for _, cat := range categories {
			if bits, ok := intentBits[cat]; ok {
				intents |= bits
			}
		}
	}

	serverFilter := c.String("server")
	channelFilter := c.String("channel")
	includeBots := c.Bool("include-bots")

	gw := gateway.New(cl.Token, intents)
	ready, err := gw.Connect()
	if err != nil {
		return err
	}
	defer gw.Close()

	fmt.Fprintf(os.Stderr, "Connected as %s, listening for events...\n", ready.User.Username)

	return gw.Listen(func(eventType string, data json.RawMessage) {
		if !allEvents {
			cat, known := eventCategoryMap[eventType]
			if !known {
				return
			}
			matched := false
			for _, c := range categories {
				if c == cat {
					matched = true
					break
				}
			}
			if !matched {
				return
			}
		}

		var partial struct {
			GuildID   string `json:"guild_id"`
			ChannelID string `json:"channel_id"`
			Author    *struct {
				Bot bool `json:"bot"`
			} `json:"author"`
		}
		json.Unmarshal(data, &partial)

		if serverFilter != "" && partial.GuildID != serverFilter {
			return
		}
		if channelFilter != "" && partial.ChannelID != channelFilter {
			return
		}
		if !includeBots && partial.Author != nil && partial.Author.Bot {
			return
		}

		out := map[string]any{
			"t": eventType,
			"d": json.RawMessage(data),
		}
		line, _ := json.Marshal(out)
		fmt.Fprintln(os.Stdout, string(line))
	})
}
