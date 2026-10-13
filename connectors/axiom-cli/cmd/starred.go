package cmd

import "github.com/urfave/cli/v2"

// StarredCommand returns the `starred` (saved queries) subcommand tree.
func StarredCommand() *cli.Command {
	return &cli.Command{
		Name:  "starred",
		Usage: "Manage Axiom saved (starred) queries",
		Subcommands: []*cli.Command{
			{Name: "list", Usage: "List starred queries (GET /v2/starred)", Action: listAction("/v2/starred")},
			{Name: "get", Usage: "Get a starred query (GET /v2/starred/{id})", Flags: []cli.Flag{idFlag()}, Action: getByID("/v2/starred")},
			{Name: "create", Usage: "Create a starred query (POST /v2/starred)", Flags: []cli.Flag{bodyFlag()}, Action: postBody("/v2/starred")},
			{Name: "update", Usage: "Update a starred query (PUT /v2/starred/{id})", Flags: []cli.Flag{idFlag(), bodyFlag()}, Action: putByID("/v2/starred")},
			{Name: "delete", Usage: "Delete a starred query (DELETE /v2/starred/{id})", Flags: []cli.Flag{idFlag()}, Action: deleteByID("/v2/starred")},
		},
	}
}
