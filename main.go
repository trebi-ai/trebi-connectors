package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v2"

	"github.com/flarco/cli-tools/discord-cli/cmd"
	"github.com/flarco/cli-tools/discord-cli/internal/client"
	"github.com/flarco/cli-tools/discord-cli/internal/config"
)

var version = "dev"

func main() {
	app := &cli.App{
		Name:    "discord-cli",
		Usage:   "Discord CLI tool",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "token",
				EnvVars: []string{"DISCORD_BOT_TOKEN"},
				Usage:   "Discord bot token",
			},
			&cli.BoolFlag{
				Name:    "json",
				Aliases: []string{"j"},
				Usage:   "output as JSON",
			},
		},
		Before: beforeHook,
		Commands: []*cli.Command{
			cmd.AuthCommand(),
			cmd.MessageCommand(),
			cmd.ReactionCommand(),
			cmd.ThreadCommand(),
			cmd.ChannelCommand(),
			cmd.ServerCommand(),
			cmd.ListenCommand(),
		},
		Metadata: map[string]any{},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func beforeHook(c *cli.Context) error {
	if wd, err := os.Getwd(); err == nil {
		for dir := wd; ; {
			if loaded := config.LoadDotEnv(filepath.Join(dir, ".env")); loaded {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	token := c.String("token")
	if token == "" {
		token = os.Getenv("DISCORD_BOT_TOKEN")
	}
	if token == "" {
		cfg, err := config.Load()
		if err == nil {
			token = cfg.Token
		}
	}

	if token != "" {
		c.App.Metadata["client"] = client.New(token)
	}
	return nil
}
