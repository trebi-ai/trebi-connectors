package cmd

import (
	"cmp"
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/trebi-ai/trebi-connectors/sdk"
	cli "github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/fakeplane"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/serve"
)

// sandboxKey is the key that the sandbox sends to the fake. A real key never goes there.
const sandboxKey = "sandbox"

// ServeCommand returns `serve`, the trebi-connector/1 adapter on stdin and stdout.
func ServeCommand() *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Serve the trebi-connector/1 protocol on stdin and stdout (for Trebi)",
		Description: `Polls Plane and sends changes as events: work_item.created,
work_item.updated, work_item.state_changed, comment.created, intake.created,
intake.accepted, intake.declined, intake.duplicate, intake.snoozed, and
customer.updated. The room of an event is <workspace>/<project identifier>.

--projects (PLANE_PROJECTS) sets the scope, for example
"acme/*, beta/ENG". Each workspace uses its key from the config
file ([workspaces.<slug>]), or PLANE_API_KEY. In Trebi mode (TREBI_STATE_DIR
is set), each workspace uses PLANE_API_KEY.

--sandbox runs against a fake Plane in this process, with one scripted
change each second (for conformance checks).`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "projects", Sources: cli.EnvVars(serve.InputProjects), Usage: "Workspaces and projects to watch, for example 'acme/*, beta/ENG'"},
			&cli.DurationFlag{Name: "interval", Sources: cli.EnvVars("PLANE_POLL_INTERVAL"), Usage: "Time between polls (default 60s; 1s with --sandbox)"},
			&cli.BoolFlag{Name: "sandbox", Usage: "Run against a fake Plane in this process (for conformance checks)"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			if err := config.RefuseLongRun("serve"); err != nil {
				return err
			}
			return runServe(ctx, c)
		},
	}
}

func runServe(ctx context.Context, c *cli.Command) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg := FromCmd(c).Config
	opt := serve.Options{Version: c.Root().Version, Interval: c.Duration("interval")}
	if c.Bool("sandbox") {
		fake := fakeplane.Start()
		defer fake.Close()
		fake.Auto(time.Second)
		// In Trebi the sandbox still needs the Projects input, so that a
		// check with no inputs sees the missing input. The fake has one workspace.
		if _, inTrebi := sdk.FromEnv(); !inTrebi || strings.TrimSpace(c.String("projects")) != "" {
			opt.Scope = serve.Scope{fakeplane.Workspace: nil}
		}
		opt.Interval = cmp.Or(opt.Interval, time.Second)
		sc := &config.Config{Host: fake.URL(), APIKey: sandboxKey}
		opt.Workspace = func(string) serve.Workspace {
			return serve.Workspace{Client: client.New(sc), HasKey: true, Web: fake.URL()}
		}
		return sdk.Serve(ctx, serve.New(opt))
	}
	if s := strings.TrimSpace(c.String("projects")); s != "" {
		opt.Scope, opt.ScopeErr = serve.ParseScope(s)
	}
	opt.Workspace = workspaceAccess(cfg)
	return sdk.Serve(ctx, serve.New(opt))
}

// workspaceAccess gives the client of each workspace, with its own key.
func workspaceAccess(cfg *config.Config) func(string) serve.Workspace {
	return func(slug string) serve.Workspace {
		wc := cfg.ForWorkspace(slug)
		return serve.Workspace{Client: client.New(wc), HasKey: strings.TrimSpace(wc.APIKey) != "", Web: serve.WebURL(wc.BaseURL())}
	}
}
