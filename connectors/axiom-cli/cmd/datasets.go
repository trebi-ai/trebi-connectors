package cmd

import (
	"fmt"

	"github.com/urfave/cli/v2"
)

// DatasetsCommand returns the `datasets` subcommand tree, which also covers
// dataset-scoped resources (fields, map-fields, tags, metrics, trim, vacuum).
func DatasetsCommand() *cli.Command {
	return &cli.Command{
		Name:  "datasets",
		Usage: "Manage Axiom datasets, fields, tags, metrics, and dataset maintenance",
		Subcommands: []*cli.Command{
			{Name: "list", Usage: "List datasets (GET /v2/datasets)", Action: listAction("/v2/datasets")},
			{Name: "get", Usage: "Get a dataset by id (GET /v2/datasets/{id})", Flags: []cli.Flag{idFlag()}, Action: getByID("/v2/datasets")},
			{Name: "create", Usage: "Create a dataset (POST /v2/datasets)", Flags: []cli.Flag{bodyFlag()}, Action: postBody("/v2/datasets")},
			{Name: "update", Usage: "Update a dataset (PUT /v2/datasets/{id})", Flags: []cli.Flag{idFlag(), bodyFlag()}, Action: putByID("/v2/datasets")},
			{Name: "delete", Usage: "Delete a dataset (DELETE /v2/datasets/{id})", Flags: []cli.Flag{idFlag()}, Action: deleteByID("/v2/datasets")},

			{
				Name:  "trim",
				Usage: "Trim events older than --max-duration (POST /v1/datasets/{name}/trim)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "name", Usage: "Dataset name", Required: true},
					&cli.StringFlag{Name: "max-duration", Usage: "e.g. 24h, 7d", Required: true},
				},
				Action: func(c *cli.Context) error {
					cl, err := newClient(c)
					if err != nil {
						return err
					}
					body := map[string]any{"maxDuration": c.String("max-duration")}
					raw, err := cl.Post(ctxOf(c), "/v1/datasets/"+c.String("name")+"/trim", body)
					if err != nil {
						return err
					}
					return emit(c, raw)
				},
			},
			{
				Name:  "vacuum",
				Usage: "Vacuum a dataset (POST /v1/datasets/{name}/vacuum)",
				Flags: []cli.Flag{&cli.StringFlag{Name: "name", Required: true, Usage: "Dataset name"}},
				Action: func(c *cli.Context) error {
					cl, err := newClient(c)
					if err != nil {
						return err
					}
					raw, err := cl.Post(ctxOf(c), "/v1/datasets/"+c.String("name")+"/vacuum", nil)
					if err != nil {
						return err
					}
					return emit(c, raw)
				},
			},

			datasetsFieldsCommand(),
			datasetsMapFieldsCommand(),
			datasetsTagsCommand(),
		},
	}
}

func datasetsFieldsCommand() *cli.Command {
	return &cli.Command{
		Name:  "fields",
		Usage: "Manage dataset fields (GET /v2/datasets/{id}/fields)",
		Subcommands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List fields for a dataset",
				Flags: []cli.Flag{&cli.StringFlag{Name: "dataset", Required: true}},
				Action: func(c *cli.Context) error {
					cl, err := newClient(c)
					if err != nil {
						return err
					}
					raw, err := cl.Get(ctxOf(c), "/v2/datasets/"+c.String("dataset")+"/fields", nil)
					if err != nil {
						return err
					}
					return emit(c, raw)
				},
			},
			{
				Name:  "get",
				Usage: "Get a single field",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "dataset", Required: true},
					&cli.StringFlag{Name: "field", Required: true},
				},
				Action: func(c *cli.Context) error {
					cl, err := newClient(c)
					if err != nil {
						return err
					}
					path := fmt.Sprintf("/v2/datasets/%s/fields/%s", c.String("dataset"), c.String("field"))
					raw, err := cl.Get(ctxOf(c), path, nil)
					if err != nil {
						return err
					}
					return emit(c, raw)
				},
			},
			{
				Name:  "update",
				Usage: "Update a field's metadata",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "dataset", Required: true},
					&cli.StringFlag{Name: "field", Required: true},
					bodyFlag(),
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
					path := fmt.Sprintf("/v2/datasets/%s/fields/%s", c.String("dataset"), c.String("field"))
					raw, err := cl.Put(ctxOf(c), path, body)
					if err != nil {
						return err
					}
					return emit(c, raw)
				},
			},
		},
	}
}

func datasetsMapFieldsCommand() *cli.Command {
	return &cli.Command{
		Name:  "map-fields",
		Usage: "Manage map fields for a dataset (GET/POST/PUT/DELETE /v2/datasets/{id}/mapfields)",
		Subcommands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List map fields",
				Flags: []cli.Flag{&cli.StringFlag{Name: "dataset", Required: true}},
				Action: func(c *cli.Context) error {
					cl, err := newClient(c)
					if err != nil {
						return err
					}
					raw, err := cl.Get(ctxOf(c), "/v2/datasets/"+c.String("dataset")+"/mapfields", nil)
					if err != nil {
						return err
					}
					return emit(c, raw)
				},
			},
			{
				Name:  "create",
				Usage: "Create a map field",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "dataset", Required: true},
					bodyFlag(),
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
					raw, err := cl.Post(ctxOf(c), "/v2/datasets/"+c.String("dataset")+"/mapfields", body)
					if err != nil {
						return err
					}
					return emit(c, raw)
				},
			},
			{
				Name:  "update",
				Usage: "Replace map fields for a dataset",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "dataset", Required: true},
					bodyFlag(),
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
					raw, err := cl.Put(ctxOf(c), "/v2/datasets/"+c.String("dataset")+"/mapfields", body)
					if err != nil {
						return err
					}
					return emit(c, raw)
				},
			},
			{
				Name:  "delete",
				Usage: "Delete a map field",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "dataset", Required: true},
					&cli.StringFlag{Name: "name", Required: true},
				},
				Action: func(c *cli.Context) error {
					cl, err := newClient(c)
					if err != nil {
						return err
					}
					path := fmt.Sprintf("/v2/datasets/%s/mapfields/%s", c.String("dataset"), c.String("name"))
					raw, err := cl.Delete(ctxOf(c), path)
					if err != nil {
						return err
					}
					return emit(c, raw)
				},
			},
		},
	}
}

func datasetsTagsCommand() *cli.Command {
	return &cli.Command{
		Name:  "tags",
		Usage: "Inspect dataset/metric tags & metric metadata",
		Subcommands: []*cli.Command{
			{Name: "list", Usage: "List dataset tags (GET /v1/datasets/_tags)", Action: listAction("/v1/datasets/_tags")},
			{Name: "values", Usage: "List dataset tag values (GET /v1/datasets/_tags/values)", Action: listAction("/v1/datasets/_tags/values")},
			{Name: "metrics", Usage: "List metric names (GET /v1/datasets/_metrics)", Action: listAction("/v1/datasets/_metrics")},
			{Name: "metric-tags", Usage: "List metric tags (GET /v1/datasets/_metrics/tags)", Action: listAction("/v1/datasets/_metrics/tags")},
			{Name: "metric-tag-values", Usage: "List metric tag values (GET /v1/datasets/_metrics/tags/values)", Action: listAction("/v1/datasets/_metrics/tags/values")},
		},
	}
}
