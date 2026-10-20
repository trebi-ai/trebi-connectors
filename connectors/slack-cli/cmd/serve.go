package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	cli "github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/sdk"

	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/fakeslack"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/serve"
)

// EnvWorkspaces is the connection input that selects the workspaces.
const EnvWorkspaces = "SLACK_WORKSPACES"

// ServeCommand serves the trebi-connector/1 protocol on stdin and stdout.
func ServeCommand(version string) *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Serve the trebi-connector/1 protocol on stdin and stdout (for Trebi)",
		Description: `Streams the events of the selected workspaces to Trebi. Stdout belongs to
the protocol; logs go to stderr.

The workspaces come from the config. --workspaces (SLACK_WORKSPACES) is a
comma-separated list of names, team ids, or domains; "all" selects every
workspace, and an empty list selects the default workspace. A workspace needs
a session token (auth import) or an app token (Socket Mode).

--sandbox runs the adapter against a fake Slack in this process, for the
Trebi conformance check. It reads and writes no Slack config.`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "workspaces", Sources: cli.EnvVars(EnvWorkspaces), Usage: "Workspaces to stream (comma-separated; all for every workspace)"},
			&cli.BoolFlag{Name: "sandbox", Usage: "Run against a fake Slack in this process (for conformance checks)"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			a := FromCmd(c)
			cacheDir := config.CacheDir()
			if c.Bool("sandbox") && !config.Trebi() {
				cacheDir = ""
			}
			cfg, list := a.Config, c.String("workspaces")
			var fake *fakeslack.Server
			if c.Bool("sandbox") {
				fake = fakeslack.Start()
				defer fake.Close()
				cfg, list = fake.Config(), "all"
			}
			names, missing := cfg.Pick(list)
			var problems []string
			if len(missing) > 0 {
				problems = append(problems, "no Slack login for the workspace "+strings.Join(missing, ", ")+"; "+config.LoginHint())
			}
			var teams []*serve.Team
			refresh := &sessionRefresher{cfg: cfg}
			for _, name := range names {
				w := cfg.Workspaces[name]
				cl := client.New(w)
				cl.Verbose = a.Verbose
				wsURL := ""
				if fake != nil {
					cl.SetBaseURL(fake.APIURL())
					wsURL = fake.WSURL()
				} else {
					refresh.attach(cl, name, w)
				}
				team := serve.NewTeam(name, w, cl, client.NewDirectory(cl, cacheDir, name))
				if err := team.Listen(w, wsURL); err != nil {
					slog.Warn("skip workspace", "err", err)
					problems = append(problems, err.Error())
					continue
				}
				teams = append(teams, team)
			}
			if len(names) == 0 && len(missing) == 0 {
				problems = append(problems, "no Slack login; "+config.LoginHint())
			}
			return sdk.Serve(ctx, serve.New(version, teams, strings.Join(problems, "; ")))
		},
	}
}

// sessionRefresher mints a new session token from the cookie and saves it.
// The teams share one config, so one lock guards the saves.
type sessionRefresher struct {
	cfg *config.Config
	mu  sync.Mutex
}

func (r *sessionRefresher) attach(cl *client.Client, name string, w config.Workspace) {
	if w.Kind() != "session" || w.Cookie == "" || (w.Domain == "" && w.URL == "") {
		return
	}
	var refresh func(ctx context.Context) (config.Workspace, error)
	refresh = func(ctx context.Context) (config.Workspace, error) {
		cl.Refresh = refresh // Call clears it; serve runs for days
		token, err := mintToken(ctx, cl.HTTP(), w)
		if err != nil {
			return config.Workspace{}, fmt.Errorf("mint a session token for %s: %w", name, err)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		w.Token = token
		r.cfg.Set(name, w)
		return w, r.cfg.Save()
	}
	cl.Refresh = refresh
}
