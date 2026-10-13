package cmd

import "github.com/urfave/cli/v2"

// GroupsCommand returns the `groups` subcommand tree.
func GroupsCommand() *cli.Command {
	return &cli.Command{
		Name:  "groups",
		Usage: "Manage Axiom groups",
		Subcommands: []*cli.Command{
			{Name: "list", Usage: "List groups (GET /v2/groups)", Action: listAction("/v2/groups")},
			{Name: "get", Usage: "Get a group (GET /v2/groups/{id})", Flags: []cli.Flag{idFlag()}, Action: getByID("/v2/groups")},
			{Name: "create", Usage: "Create a group (POST /v2/groups)", Flags: []cli.Flag{bodyFlag()}, Action: postBody("/v2/groups")},
			{Name: "update", Usage: "Update a group (PUT /v2/groups/{id})", Flags: []cli.Flag{idFlag(), bodyFlag()}, Action: putByID("/v2/groups")},
			{Name: "delete", Usage: "Delete a group (DELETE /v2/groups/{id})", Flags: []cli.Flag{idFlag()}, Action: deleteByID("/v2/groups")},
		},
	}
}
