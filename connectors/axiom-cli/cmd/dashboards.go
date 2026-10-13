package cmd

import "github.com/urfave/cli/v2"

// DashboardsCommand returns the `dashboards` subcommand tree.
func DashboardsCommand() *cli.Command {
	return &cli.Command{
		Name:  "dashboards",
		Usage: "Manage Axiom dashboards",
		Subcommands: []*cli.Command{
			{Name: "list", Usage: "List dashboards (GET /v2/dashboards)", Action: listAction("/v2/dashboards")},
			{Name: "get", Usage: "Get a dashboard (GET /v2/dashboards/{id})", Flags: []cli.Flag{idFlag()}, Action: getByID("/v2/dashboards")},
			{Name: "create", Usage: "Create a dashboard (POST /v2/dashboards)", Flags: []cli.Flag{bodyFlag()}, Action: postBody("/v2/dashboards")},
			{Name: "update", Usage: "Update a dashboard (PUT /v2/dashboards/{id})", Flags: []cli.Flag{idFlag(), bodyFlag()}, Action: putByID("/v2/dashboards")},
			{Name: "delete", Usage: "Delete a dashboard (DELETE /v2/dashboards/{id})", Flags: []cli.Flag{idFlag()}, Action: deleteByID("/v2/dashboards")},
		},
	}
}
