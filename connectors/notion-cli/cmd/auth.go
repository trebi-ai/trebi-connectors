package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/notion-cli/internal/config"
)

// AuthCommand returns the `auth` subcommand tree.
func AuthCommand() *cli.Command {
	return &cli.Command{
		Name:  "auth",
		Usage: "Manage the Notion integration secret",
		Subcommands: []*cli.Command{
			{Name: "set", Usage: "Save the secret to the config file", ArgsUsage: "<secret>", Action: authSet},
			{Name: "show", Usage: "Show the secret source and a masked value", Action: authShow},
			{Name: "test", Usage: "Test the secret with GET /v1/users/me", Action: authTest},
		},
	}
}

func authSet(c *cli.Context) error {
	token := c.Args().First()
	if token == "" {
		return errors.New("usage: notion-cli auth set <secret>")
	}
	if err := config.Save(token); errors.Is(err, config.ErrSetInTrebi) {
		return err
	} else if err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	fmt.Fprintln(os.Stdout, "Secret saved to", config.Path())
	return nil
}

func authShow(c *cli.Context) error {
	s := settingsFromCtx(c)
	if s.Token == "" {
		fmt.Fprintln(os.Stdout, "No secret configured")
		return nil
	}
	masked := "****"
	if len(s.Token) > 10 {
		masked = s.Token[:6] + "…" + s.Token[len(s.Token)-4:]
	}
	fmt.Fprintf(os.Stdout, "Secret: %s\nSource: %s\n", masked, s.Source)
	return nil
}

func authTest(c *cli.Context) error {
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	me, err := cl.Me(c.Context)
	if err != nil {
		return fmt.Errorf("auth test failed: %w", err)
	}
	if c.Bool("json") {
		return printJSON(os.Stdout, me)
	}
	fmt.Fprintf(os.Stdout, "Authenticated as %s (ID: %s)\n", me.Name, me.ID)
	return nil
}
