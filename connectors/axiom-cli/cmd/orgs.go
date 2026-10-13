package cmd

import "github.com/urfave/cli/v2"

// OrgsCommand returns the `orgs` subcommand tree.
func OrgsCommand() *cli.Command {
	return &cli.Command{
		Name:  "orgs",
		Usage: "Inspect organizations accessible to the token",
		Subcommands: []*cli.Command{
			{Name: "list", Usage: "List orgs (GET /v2/orgs)", Action: listAction("/v2/orgs")},
			{Name: "get", Usage: "Get an org (GET /v2/orgs/{id})", Flags: []cli.Flag{idFlag()}, Action: getByID("/v2/orgs")},
			{Name: "update", Usage: "Update an org (PUT /v2/orgs/{id})", Flags: []cli.Flag{idFlag(), bodyFlag()}, Action: putByID("/v2/orgs")},
		},
	}
}
