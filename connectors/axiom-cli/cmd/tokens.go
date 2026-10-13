package cmd

import "github.com/urfave/cli/v2"

// TokensCommand returns the `tokens` subcommand tree.
func TokensCommand() *cli.Command {
	return &cli.Command{
		Name:  "tokens",
		Usage: "Manage Axiom API tokens",
		Subcommands: []*cli.Command{
			{Name: "list", Usage: "List tokens (GET /v2/tokens)", Action: listAction("/v2/tokens")},
			{Name: "get", Usage: "Get a token (GET /v2/tokens/{id})", Flags: []cli.Flag{idFlag()}, Action: getByID("/v2/tokens")},
			{Name: "create", Usage: "Create a token (POST /v2/tokens)", Flags: []cli.Flag{bodyFlag()}, Action: postBody("/v2/tokens")},
			{Name: "delete", Usage: "Delete a token (DELETE /v2/tokens/{id})", Flags: []cli.Flag{idFlag()}, Action: deleteByID("/v2/tokens")},
			{
				Name:  "regenerate",
				Usage: "Regenerate a token (POST /v2/tokens/{id}/regenerate)",
				Flags: []cli.Flag{idFlag(), bodyFlag()},
				Action: func(c *cli.Context) error {
					body, err := readBody(c, "body")
					if err != nil {
						return err
					}
					cl, err := newClient(c)
					if err != nil {
						return err
					}
					raw, err := cl.Post(ctxOf(c), "/v2/tokens/"+c.String("id")+"/regenerate", body)
					if err != nil {
						return err
					}
					return emit(c, raw)
				},
			},
		},
	}
}
