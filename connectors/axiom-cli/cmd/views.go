package cmd

import "github.com/urfave/cli/v2"

// ViewsCommand returns the `views` subcommand tree.
func ViewsCommand() *cli.Command {
	return &cli.Command{
		Name:  "views",
		Usage: "Manage Axiom views",
		Subcommands: []*cli.Command{
			{Name: "list", Usage: "List views (GET /v2/views)", Action: listAction("/v2/views")},
			{Name: "get", Usage: "Get a view (GET /v2/views/{id})", Flags: []cli.Flag{idFlag()}, Action: getByID("/v2/views")},
			{Name: "create", Usage: "Create a view (POST /v2/views)", Flags: []cli.Flag{bodyFlag()}, Action: postBody("/v2/views")},
			{Name: "update", Usage: "Update a view (PUT /v2/views/{id})", Flags: []cli.Flag{idFlag(), bodyFlag()}, Action: putByID("/v2/views")},
			{Name: "delete", Usage: "Delete a view (DELETE /v2/views/{id})", Flags: []cli.Flag{idFlag()}, Action: deleteByID("/v2/views")},
		},
	}
}
