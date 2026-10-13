package cmd

import (
	"github.com/urfave/cli/v2"
)

// MonitorsCommand returns the `monitors` subcommand tree.
func MonitorsCommand() *cli.Command {
	return &cli.Command{
		Name:  "monitors",
		Usage: "Manage Axiom monitors and inspect their history",
		Subcommands: []*cli.Command{
			{Name: "list", Usage: "List monitors (GET /v2/monitors)", Action: listAction("/v2/monitors")},
			{Name: "get", Usage: "Get a monitor (GET /v2/monitors/{id})", Flags: []cli.Flag{idFlag()}, Action: getByID("/v2/monitors")},
			{Name: "create", Usage: "Create a monitor (POST /v2/monitors)", Flags: []cli.Flag{bodyFlag()}, Action: postBody("/v2/monitors")},
			{Name: "update", Usage: "Update a monitor (PUT /v2/monitors/{id})", Flags: []cli.Flag{idFlag(), bodyFlag()}, Action: putByID("/v2/monitors")},
			{Name: "delete", Usage: "Delete a monitor (DELETE /v2/monitors/{id})", Flags: []cli.Flag{idFlag()}, Action: deleteByID("/v2/monitors")},
			{
				Name:  "history",
				Usage: "Get monitor evaluation history (GET /v2/monitors/{id}/history?startTime=&endTime=)",
				Flags: []cli.Flag{
					idFlag(),
					&cli.StringFlag{Name: "startTime", Usage: "RFC3339 / ISO 8601"},
					&cli.StringFlag{Name: "endTime", Usage: "RFC3339 / ISO 8601"},
				},
				Action: func(c *cli.Context) error {
					cl, err := newClient(c)
					if err != nil {
						return err
					}
					q := buildQuery(c, []string{"startTime", "endTime"})
					raw, err := cl.Get(ctxOf(c), "/v2/monitors/"+c.String("id")+"/history", q)
					if err != nil {
						return err
					}
					return emit(c, raw)
				},
			},
		},
	}
}
