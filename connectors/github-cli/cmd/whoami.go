package cmd

import (
	"fmt"
	"strings"

	"github.com/urfave/cli/v2"
)

// WhoamiCommand shows the account of the token. The daemon runs it to test
// a pasted token.
func WhoamiCommand() *cli.Command {
	return &cli.Command{
		Name:  "whoami",
		Usage: "Show the GitHub account of the token, with its scopes",
		Action: func(c *cli.Context) error {
			gh, err := clientFromCtx(c)
			if err != nil {
				return err
			}
			id, err := gh.Whoami(c.Context)
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return printJSON(c.App.Writer, map[string]any{"login": id.Login, "name": id.Name, "scopes": id.Scopes, "scoped": id.Scoped})
			}
			scopes := "none (a fine-grained token or a GitHub App)"
			if id.Scoped {
				scopes = strings.Join(id.Scopes, ", ")
			}
			fmt.Fprintf(c.App.Writer, "login:  %s\nname:   %s\nscopes: %s\n", id.Login, id.Name, scopes)
			return nil
		},
	}
}
