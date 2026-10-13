package cmd

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/internal/client"
	"github.com/urfave/cli/v2"
)

// IngestCommand returns the `ingest` subcommand. Streams a body of JSON,
// NDJSON, or CSV into a dataset (POST /v1/datasets/{name}/ingest).
func IngestCommand() *cli.Command {
	return &cli.Command{
		Name:      "ingest",
		Usage:     "Ingest events into a dataset (POST /v1/datasets/{name}/ingest)",
		ArgsUsage: "<dataset>",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "file", Usage: "Path to body file, or - for stdin", Required: true},
			&cli.StringFlag{Name: "content-type", Usage: "application/json | application/x-ndjson | text/csv (auto-detected from file extension if omitted)"},
			&cli.StringFlag{Name: "timestamp-field", Usage: "Field name to use as the event timestamp"},
			&cli.StringFlag{Name: "timestamp-format", Usage: "Go-style timestamp format (e.g. 2006-01-02T15:04:05Z)"},
			&cli.StringFlag{Name: "csv-delimiter", Usage: "CSV delimiter character (default ,)"},
			&cli.StringFlag{Name: "csv-fields", Usage: "Comma-separated CSV header field names (X-Axiom-CSV-Fields)"},
			&cli.StringFlag{Name: "event-labels", Usage: "JSON-encoded object of additional event labels (X-Axiom-Event-Labels)"},
		},
		Action: runIngest,
	}
}

func runIngest(c *cli.Context) error {
	if c.NArg() < 1 {
		return fmt.Errorf("provide the dataset name: axiom-cli ingest <dataset> --file path|-")
	}
	dataset := c.Args().First()
	src := c.String("file")

	var body io.Reader
	if src == "-" {
		body = os.Stdin
	} else {
		f, err := os.Open(src)
		if err != nil {
			return fmt.Errorf("open %s: %w", src, err)
		}
		defer f.Close()
		body = f
	}

	contentType := c.String("content-type")
	if contentType == "" {
		contentType = detectContentType(src)
	}

	q := url.Values{}
	for _, n := range []string{"timestamp-field", "timestamp-format", "csv-delimiter"} {
		if v := c.String(n); v != "" {
			q.Set(n, v)
		}
	}

	headers := map[string]string{}
	if v := c.String("csv-fields"); v != "" {
		headers["X-Axiom-CSV-Fields"] = v
	}
	if v := c.String("event-labels"); v != "" {
		headers["X-Axiom-Event-Labels"] = v
	}

	cl, err := newClient(c)
	if err != nil {
		return err
	}

	raw, err := cl.Do(ctxOf(c), "POST", "/v1/datasets/"+dataset+"/ingest", &client.Options{
		BodyRaw:     body,
		ContentType: contentType,
		Query:       q,
		Headers:     headers,
	})
	if err != nil {
		return err
	}
	return emit(c, raw)
}

func detectContentType(path string) string {
	switch {
	case strings.HasSuffix(path, ".ndjson"), strings.HasSuffix(path, ".jsonl"):
		return "application/x-ndjson"
	case strings.HasSuffix(path, ".csv"):
		return "text/csv"
	default:
		return "application/json"
	}
}
