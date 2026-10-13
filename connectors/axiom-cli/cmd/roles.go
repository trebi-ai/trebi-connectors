package cmd

import "github.com/urfave/cli/v2"

// RolesCommand returns the `roles` subcommand tree.
func RolesCommand() *cli.Command {
	return &cli.Command{
		Name:  "roles",
		Usage: "Manage Axiom roles",
		Subcommands: []*cli.Command{
			{Name: "list", Usage: "List roles (GET /v2/roles)", Action: listAction("/v2/roles")},
			{Name: "get", Usage: "Get a role (GET /v2/roles/{id})", Flags: []cli.Flag{idFlag()}, Action: getByID("/v2/roles")},
			{Name: "create", Usage: "Create a role (POST /v2/roles)", Flags: []cli.Flag{bodyFlag()}, Action: postBody("/v2/roles")},
			{Name: "update", Usage: "Update a role (PUT /v2/roles/{id})", Flags: []cli.Flag{idFlag(), bodyFlag()}, Action: putByID("/v2/roles")},
			{Name: "delete", Usage: "Delete a role (DELETE /v2/roles/{id})", Flags: []cli.Flag{idFlag()}, Action: deleteByID("/v2/roles")},
		},
	}
}
