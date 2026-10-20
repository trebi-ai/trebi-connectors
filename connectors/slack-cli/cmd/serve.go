package cmd

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	cli "github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/sdk"

	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/fakeslack"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/localauth"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/serve"
)

// ServeCommand serves the trebi-connector/1 protocol on stdin and stdout.
func ServeCommand(version string) *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Serve the trebi-connector/1 protocol on stdin and stdout (for Trebi)",
		Description: `Streams the events of every saved workspace to Trebi. Stdout belongs to
the protocol; logs go to stderr.

Trebi adds the workspaces with the login methods: the Slack desktop app, the
slk tokens, a pasted token, or a pasted browser session. A workspace with a
xoxp-/xoxb- token gives actions only; live events need a session token or
an app token.

--sandbox runs the adapter against a fake Slack in this process, for the
Trebi conformance check. Its existing login is the fake workspace. Outside
Trebi mode it keeps the workspaces in a temp folder.`,
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "sandbox", Usage: "Run against a fake Slack in this process (for conformance checks)"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			a := FromCmd(c)
			opts := serve.Options{
				Version:  version,
				Store:    serve.NewStore(a.Config),
				CacheDir: config.CacheDir(),
				Mint:     mintToken,
				Client: func(w config.Workspace) *client.Client {
					cl := client.New(w)
					cl.Verbose = a.Verbose
					return cl
				},
				Local: localLogins(),
			}
			if c.Bool("sandbox") {
				fake := fakeslack.Start()
				defer fake.Close()
				if !config.Trebi() {
					dir, err := os.MkdirTemp("", "slack-cli-sandbox-*")
					if err != nil {
						return err
					}
					defer os.RemoveAll(dir)
					opts.Store, opts.CacheDir = serve.NewStore(config.New(filepath.Join(dir, "config.toml"))), ""
				}
				opts.Mint, opts.WSURL = nil, fake.WSURL()
				opts.Local = map[string]serve.Local{serve.MethodDesktop: fakeslack.Local{}}
				opts.Client = func(w config.Workspace) *client.Client {
					cl := client.New(w)
					cl.SetBaseURL(fake.APIURL())
					return cl
				}
			}
			return sdk.Serve(ctx, serve.New(opts))
		},
	}
}

// localLogins are the Slack logins on this computer.
func localLogins() map[string]serve.Local {
	out := map[string]serve.Local{serve.MethodSlk: localauth.Slk{Dir: localauth.SlkDir()}}
	if d, err := localauth.NewDesktop(); err == nil {
		out[serve.MethodDesktop] = d
	}
	return out
}
