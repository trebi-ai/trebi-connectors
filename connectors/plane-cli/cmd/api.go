package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	cli "github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/client"
)

// APICommand returns `api`, a raw request passthrough.
func APICommand() *cli.Command {
	return &cli.Command{
		Name:      "api",
		Usage:     "Send a raw request to any Plane API path",
		ArgsUsage: "<METHOD> <path>",
		Description: `Sends one authenticated request and prints the response.

A path without the /api/ prefix gets /api/v2/ in front. These placeholders are
filled from the resolved config: {workspace} / {slug} and {project} / {project_id}.

Examples:
  plane-cli api GET 'workspaces/{workspace}/projects/'
  plane-cli api GET /api/v1/workspaces/{workspace}/projects/ --all
  plane-cli api PATCH 'workspaces/{workspace}/projects/{project}/work-items/ENG-12/' -d '{"priority":"high"}'`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "data", Aliases: []string{"d"}, Usage: "Request body as JSON (or @file, - for stdin)"},
			&cli.StringSliceFlag{Name: "filter", Aliases: []string{"q"}, Usage: "Query parameter as key=value (repeatable)"},
			&cli.BoolFlag{Name: "all", Aliases: []string{"a"}, Usage: "GET only: follow pagination and return every item"},
			&cli.IntFlag{Name: "max", Usage: "With --all, stop after this many items"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			if c.Args().Len() != 2 {
				return fmt.Errorf("expected <METHOD> <path>")
			}
			method := strings.ToUpper(c.Args().Get(0))
			path := c.Args().Get(1)
			if !strings.HasPrefix(path, "/api/") {
				path = "/api/v2/" + strings.TrimPrefix(path, "/")
			}
			repl := strings.NewReplacer(
				"{workspace}", url.PathEscape(a.Config.Workspace), "{slug}", url.PathEscape(a.Config.Workspace),
				"{project}", url.PathEscape(a.Config.Project), "{project_id}", url.PathEscape(a.Config.Project),
			)
			path = repl.Replace(path)
			q := url.Values{}
			for _, kv := range c.StringSlice("filter") {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("invalid --filter %q (expected key=value)", kv)
				}
				q.Add(k, v)
			}
			var body any
			if raw := c.String("data"); raw != "" {
				v, err := parseJSON(raw)
				if err != nil {
					return fmt.Errorf("--data: %w", err)
				}
				body = v
			}
			cl, err := a.Client()
			if err != nil {
				return err
			}
			w := c.Root().Writer
			if method == http.MethodGet && c.Bool("all") {
				items, err := cl.ListAll(ctx, path, q, strings.HasPrefix(path, "/api/v2/"), int(c.Int("max")))
				if err != nil {
					return err
				}
				return a.RenderItems(w, items)
			}
			resp, err := cl.Do(ctx, client.Request{Method: method, Path: path, Query: q, Body: body})
			if err != nil {
				return err
			}
			if len(strings.TrimSpace(string(resp.Body))) == 0 {
				successf(a, w, "%s %s (%d)", method, path, resp.Status)
				return nil
			}
			return a.Render(w, resp.Body)
		},
	}
}
