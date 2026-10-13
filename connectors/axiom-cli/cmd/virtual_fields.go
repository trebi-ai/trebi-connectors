package cmd

import "github.com/urfave/cli/v2"

// VirtualFieldsCommand returns the `virtual-fields` subcommand tree.
func VirtualFieldsCommand() *cli.Command {
	return &cli.Command{
		Name:  "virtual-fields",
		Usage: "Manage Axiom virtual fields",
		Subcommands: []*cli.Command{
			{Name: "list", Usage: "List virtual fields (GET /v2/vfields)", Action: listAction("/v2/vfields")},
			{Name: "get", Usage: "Get a virtual field (GET /v2/vfields/{id})", Flags: []cli.Flag{idFlag()}, Action: getByID("/v2/vfields")},
			{Name: "create", Usage: "Create a virtual field (POST /v2/vfields)", Flags: []cli.Flag{bodyFlag()}, Action: postBody("/v2/vfields")},
			{Name: "update", Usage: "Update a virtual field (PUT /v2/vfields/{id})", Flags: []cli.Flag{idFlag(), bodyFlag()}, Action: putByID("/v2/vfields")},
			{Name: "delete", Usage: "Delete a virtual field (DELETE /v2/vfields/{id})", Flags: []cli.Flag{idFlag()}, Action: deleteByID("/v2/vfields")},
		},
	}
}
