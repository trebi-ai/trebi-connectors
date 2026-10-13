package cmd

import "github.com/urfave/cli/v2"

// NotifiersCommand returns the `notifiers` subcommand tree.
func NotifiersCommand() *cli.Command {
	return &cli.Command{
		Name:  "notifiers",
		Usage: "Manage Axiom notifiers",
		Subcommands: []*cli.Command{
			{Name: "list", Usage: "List notifiers (GET /v2/notifiers)", Action: listAction("/v2/notifiers")},
			{Name: "get", Usage: "Get a notifier (GET /v2/notifiers/{id})", Flags: []cli.Flag{idFlag()}, Action: getByID("/v2/notifiers")},
			{Name: "create", Usage: "Create a notifier (POST /v2/notifiers)", Flags: []cli.Flag{bodyFlag()}, Action: postBody("/v2/notifiers")},
			{Name: "update", Usage: "Update a notifier (PUT /v2/notifiers/{id})", Flags: []cli.Flag{idFlag(), bodyFlag()}, Action: putByID("/v2/notifiers")},
			{Name: "delete", Usage: "Delete a notifier (DELETE /v2/notifiers/{id})", Flags: []cli.Flag{idFlag()}, Action: deleteByID("/v2/notifiers")},
		},
	}
}
