package cmd

import (
	"fmt"
	"net/url"

	"github.com/urfave/cli/v2"
)

// QueryCommand returns the `query` subcommand tree.
func QueryCommand() *cli.Command {
	return &cli.Command{
		Name:  "query",
		Usage: "Run APL, legacy, or batch queries against Axiom",
		Subcommands: []*cli.Command{
			{
				Name:      "apl",
				Usage:     "Run an APL query (POST /v1/datasets/_apl)",
				ArgsUsage: "<APL string>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "start", Usage: "RFC3339 start time or relative (e.g. now-1h)"},
					&cli.StringFlag{Name: "end", Usage: "RFC3339 end time"},
					&cli.StringFlag{Name: "format", Value: "tabular", Usage: "tabular | legacy"},
					&cli.BoolFlag{Name: "no-cache", Usage: "Disable result cache"},
					&cli.StringFlag{Name: "cursor", Usage: "Pagination cursor"},
					&cli.BoolFlag{Name: "include-cursor", Usage: "Include cursor in results"},
				},
				Action: queryAPL,
			},
			{
				Name:  "legacy",
				Usage: "Run a legacy structured query (POST /v1/datasets/{name}/query)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "dataset", Required: true},
					bodyFlag(),
					&cli.StringFlag{Name: "saveAsKind"},
					&cli.StringFlag{Name: "streaming-duration"},
					&cli.BoolFlag{Name: "no-cache"},
				},
				Action: func(c *cli.Context) error {
					body, err := readBody(c, "body")
					if err != nil {
						return err
					}
					cl, err := newClient(c)
					if err != nil {
						return err
					}
					q := url.Values{}
					if v := c.String("saveAsKind"); v != "" {
						q.Set("saveAsKind", v)
					}
					if v := c.String("streaming-duration"); v != "" {
						q.Set("streaming-duration", v)
					}
					if c.Bool("no-cache") {
						q.Set("nocache", "true")
					}
					raw, err := cl.PostQuery(ctxOf(c), "/v1/datasets/"+c.String("dataset")+"/query", q, body)
					if err != nil {
						return err
					}
					return emit(c, raw)
				},
			},
			{
				Name:   "batch",
				Usage:  "Run a batch query (POST /v1/datasets/queryBatch)",
				Flags:  []cli.Flag{bodyFlag()},
				Action: postBody("/v1/datasets/queryBatch"),
			},
		},
	}
}

func queryAPL(c *cli.Context) error {
	if c.NArg() < 1 {
		return fmt.Errorf("provide the APL query as a positional argument: axiom-cli query apl '<APL>'")
	}
	apl := c.Args().First()

	body := map[string]any{"apl": apl}
	if v := c.String("start"); v != "" {
		body["startTime"] = v
	}
	if v := c.String("end"); v != "" {
		body["endTime"] = v
	}
	if c.Bool("no-cache") {
		body["noCache"] = true
	}
	if v := c.String("cursor"); v != "" {
		body["cursor"] = v
	}
	if c.Bool("include-cursor") {
		body["includeCursor"] = true
	}

	q := url.Values{}
	if v := c.String("format"); v != "" {
		q.Set("format", v)
	}
	if c.Bool("no-cache") {
		q.Set("nocache", "true")
	}

	cl, err := newClient(c)
	if err != nil {
		return err
	}
	raw, err := cl.PostQuery(ctxOf(c), "/v1/datasets/_apl", q, body)
	if err != nil {
		return err
	}
	return emit(c, raw)
}
