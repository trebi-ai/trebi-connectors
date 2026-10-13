package cmd

import "github.com/urfave/cli/v2"

// UsersCommand returns the `users` subcommand tree.
func UsersCommand() *cli.Command {
	return &cli.Command{
		Name:  "users",
		Usage: "Manage Axiom users",
		Subcommands: []*cli.Command{
			{Name: "list", Usage: "List users (GET /v2/users)", Action: listAction("/v2/users")},
			{Name: "get", Usage: "Get a user (GET /v2/users/{id})", Flags: []cli.Flag{idFlag()}, Action: getByID("/v2/users")},
			{Name: "create", Usage: "Invite/create a user (POST /v2/users)", Flags: []cli.Flag{bodyFlag()}, Action: postBody("/v2/users")},
			{Name: "current", Usage: "Get the current user (GET /v2/user)", Action: listAction("/v2/user")},
			{
				Name:  "update-current",
				Usage: "Update the current user (PUT /v2/user)",
				Flags: []cli.Flag{bodyFlag()},
				Action: func(c *cli.Context) error {
					body, err := readBody(c, "body")
					if err != nil {
						return err
					}
					cl, err := newClient(c)
					if err != nil {
						return err
					}
					raw, err := cl.Put(ctxOf(c), "/v2/user", body)
					if err != nil {
						return err
					}
					return emit(c, raw)
				},
			},
			{
				Name:  "update-role",
				Usage: "Update a user's role (PUT /v2/users/{id}/role)",
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
					raw, err := cl.Put(ctxOf(c), "/v2/users/"+c.String("id")+"/role", body)
					if err != nil {
						return err
					}
					return emit(c, raw)
				},
			},
			{Name: "remove", Usage: "Remove a user from the org (DELETE /v2/users/{id})", Flags: []cli.Flag{idFlag()}, Action: deleteByID("/v2/users")},
		},
	}
}
