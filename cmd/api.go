package cmd

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	cli "github.com/urfave/cli/v3"

	"github.com/flarco/cli-tools/slack-cli/internal/output"
)

// APICommand returns `api`, a raw Web API passthrough.
func APICommand() *cli.Command {
	return &cli.Command{
		Name:      "api",
		Usage:     "Call any Slack Web API method",
		ArgsUsage: "<method>",
		Description: `Sends one Web API call and prints the response.

Nested objects in --param must be pre-serialized JSON strings, because Slack's
transport is form-urlencoded. Repeat --param for more fields.

Examples:
  slack-cli api auth.test
  slack-cli api conversations.history --param channel=C0123 --param limit=5
  slack-cli api chat.postMessage --param channel=C0123 --param blocks='[{"type":"divider"}]'
  slack-cli api users.list --all`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{Name: "param", Aliases: []string{"p"}, Usage: "Parameter as key=value (repeatable)"},
			&cli.StringFlag{Name: "data", Aliases: []string{"d"}, Usage: "Params as a JSON object (or @file, - for stdin)"},
			&cli.BoolFlag{Name: "all", Aliases: []string{"a"}, Usage: "Follow cursor pagination and return every item"},
			&cli.IntFlag{Name: "max", Usage: "With --all, stop after this many items"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			method := strings.TrimSpace(c.Args().First())
			if method == "" || c.Args().Len() > 1 {
				return fmt.Errorf("expected one <method>, for example conversations.history")
			}
			params := url.Values{}
			if raw := c.String("data"); raw != "" {
				v, err := parseJSON(raw)
				if err != nil {
					return fmt.Errorf("--data: %w", err)
				}
				m, ok := v.(map[string]any)
				if !ok {
					return fmt.Errorf("--data must be a JSON object")
				}
				for k, val := range m {
					params.Set(k, toParam(val))
				}
			}
			for _, kv := range c.StringSlice("param") {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return fmt.Errorf("invalid --param %q (expected key=value)", kv)
				}
				params.Add(k, v)
			}
			cl, err := a.Client()
			if err != nil {
				return err
			}
			w := a.Out(c)
			if c.Bool("all") {
				page, err := cl.ListAll(ctx, method, params, int(c.Int("max")))
				if err != nil {
					return err
				}
				if a.JSON() || a.Raw {
					return a.RenderItems(w, page.Items)
				}
				return output.Rows(w, rawObjects(page.Items), a.Columns)
			}
			body, err := cl.Call(ctx, method, params)
			if err != nil {
				return err
			}
			return a.Render(w, body)
		},
	}
}
