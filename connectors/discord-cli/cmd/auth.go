package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/discord-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/discord-cli/internal/config"
)

// AuthCommand returns the `auth` subcommand tree.
func AuthCommand() *cli.Command {
	return &cli.Command{
		Name:  "auth",
		Usage: "Manage Discord bot token",
		Subcommands: []*cli.Command{
			{
				Name:      "set",
				Usage:     "Save bot token to config file",
				ArgsUsage: "<token>",
				Action:    authSet,
			},
			{
				Name:   "show",
				Usage:  "Show current token source and masked value",
				Action: authShow,
			},
			{
				Name:   "test",
				Usage:  "Test token by calling GET /users/@me",
				Action: authTest,
			},
		},
	}
}

func authSet(c *cli.Context) error {
	token := c.Args().First()
	if token == "" {
		return fmt.Errorf("usage: discord-cli auth set <token>")
	}
	if err := config.Save(token); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Fprintln(os.Stdout, "Token saved to", config.Path())
	return nil
}

func authShow(c *cli.Context) error {
	token := c.String("token")
	source := "flag"

	if token == "" {
		token = os.Getenv("DISCORD_BOT_TOKEN")
		source = "env (DISCORD_BOT_TOKEN)"
	}
	if token == "" {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		token = cfg.Token
		source = "config (" + config.Path() + ")"
	}
	if token == "" {
		fmt.Fprintln(os.Stdout, "No token configured")
		return nil
	}

	fmt.Fprintf(os.Stdout, "Token:  %s\nSource: %s\n", maskToken(token), source)
	return nil
}

func authTest(c *cli.Context) error {
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	var user client.User
	if err := cl.DoJSON(c.Context, "GET", "/users/@me", nil, &user); err != nil {
		return fmt.Errorf("auth test failed: %w", err)
	}
	if outputJSON(c) {
		printJSON(os.Stdout, user)
	} else {
		fmt.Fprintf(os.Stdout, "Authenticated as %s (ID: %s)\n", user.Username, user.ID)
	}
	return nil
}

func maskToken(token string) string {
	if len(token) <= 8 {
		return strings.Repeat("*", len(token))
	}
	return token[:4] + strings.Repeat("*", len(token)-8) + token[len(token)-4:]
}
