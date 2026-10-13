package cmd

import "github.com/urfave/cli/v2"

// AnnotationsCommand returns the `annotations` subcommand tree.
func AnnotationsCommand() *cli.Command {
	return &cli.Command{
		Name:  "annotations",
		Usage: "Manage Axiom annotations (deploys, releases, incidents)",
		Subcommands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List annotations (GET /v2/annotations?datasets=&start=&end=)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "datasets", Usage: "Comma-separated dataset names"},
					&cli.StringFlag{Name: "start", Usage: "RFC3339 start time"},
					&cli.StringFlag{Name: "end", Usage: "RFC3339 end time"},
				},
				Action: listAction("/v2/annotations", "datasets", "start", "end"),
			},
			{Name: "get", Usage: "Get annotation (GET /v2/annotations/{id})", Flags: []cli.Flag{idFlag()}, Action: getByID("/v2/annotations")},
			{Name: "create", Usage: "Create annotation (POST /v2/annotations)", Flags: []cli.Flag{bodyFlag()}, Action: postBody("/v2/annotations")},
			{Name: "update", Usage: "Update annotation (PUT /v2/annotations/{id})", Flags: []cli.Flag{idFlag(), bodyFlag()}, Action: putByID("/v2/annotations")},
			{Name: "delete", Usage: "Delete annotation (DELETE /v2/annotations/{id})", Flags: []cli.Flag{idFlag()}, Action: deleteByID("/v2/annotations")},
		},
	}
}
