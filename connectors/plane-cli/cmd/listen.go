package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	cli "github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/listen"
)

// ListenCommand returns `listen`, which prints change events as JSON lines.
func ListenCommand() *cli.Command {
	return &cli.Command{
		Name:  "listen",
		Usage: "Print Plane change events as JSON lines (poll, or a webhook through a tunnel)",
		Description: `Prints one JSON object for each event on stdout, until Ctrl-C.
The object has the shape of a Plane v2 webhook payload (event, entity_id,
data, previous_attributes, ...), plus "source" (poll or webhook) and "key"
(for example ENG-42).

--poll (default) lists the work items with updated_at at or after the last
change, each --interval. It needs only the API key. It sees work item
create and update. It does not see deletes, archives, comments, links, or
other resources (comments do not change updated_at). previous_attributes shows only the fields that changed
after the listen started. --activities adds the activity rows of each change.

--tunnel starts a local HTTP server and a cloudflared quick tunnel
(cloudflared must be on PATH), then creates a temporary workspace webhook to
the tunnel URL. It sees all webhook events in real time, with deletes and
previous values. Only workspace admins can create webhooks. On exit the CLI
deletes the webhook. It also deletes webhooks of earlier runs on this host
that did not stop correctly. --public-url uses your own public URL (for
example ngrok) that forwards to --port, in place of cloudflared.

A project (-p) limits the events to that project. -W listens to the workspace.

Examples:
  plane-cli listen -p ENG
  plane-cli listen -W --interval 30s --since 1h
  plane-cli listen --tunnel --events 'workitem.*'
  plane-cli listen --public-url https://abc.ngrok.app --port 8080`,
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "poll", Usage: "Poll the work item list (default)"},
			&cli.BoolFlag{Name: "tunnel", Usage: "Receive webhook events through a cloudflared quick tunnel"},
			&cli.StringFlag{Name: "public-url", Usage: "Receive webhook events at this public URL, which forwards to --port"},
			&cli.IntFlag{Name: "port", Usage: "Local port of the webhook server (0 = any free port)"},
			&cli.StringFlag{Name: "events", Aliases: []string{"e"}, Usage: "Event names to print, comma-separated globs (for example 'workitem.*,cycle.created')"},
			&cli.DurationFlag{Name: "interval", Value: 15 * time.Second, Usage: "Poll: time between polls"},
			&cli.StringFlag{Name: "since", Usage: "Poll: also print changes since this time (RFC 3339) or this long ago (for example 1h)"},
			&cli.BoolFlag{Name: "activities", Usage: "Poll: add the activity rows of each change (one more request per change)"},
			&cli.StringSliceFlag{Name: "filter", Aliases: []string{"q"}, Usage: "Poll: extra query parameter for the work item list, as key=value (repeatable)"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			if err := config.RefuseLongRun("listen"); err != nil {
				return err
			}
			return runListen(ctx, c)
		},
	}
}

func runListen(ctx context.Context, c *cli.Command) error {
	a := FromCmd(c)
	webhook := c.Bool("tunnel") || c.String("public-url") != ""
	if webhook && c.Bool("poll") {
		return errors.New("use --poll or --tunnel/--public-url, not both")
	}
	if c.Bool("tunnel") && c.String("public-url") != "" {
		return errors.New("use --tunnel or --public-url, not both")
	}
	filter, err := listen.ParseFilter(c.String("events"))
	if err != nil {
		return err
	}
	ws, err := a.Workspace()
	if err != nil {
		return err
	}
	cl, err := a.Client()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	var projectID string
	if a.Config.Project != "" && !c.Bool("workspace-scope") {
		if projectID, err = cl.ProjectID(ctx, ws, a.Config.Project); err != nil {
			return err
		}
	}
	projects := listen.NewProjects(cl, ws)
	out := listen.NewEmitter(os.Stdout)

	if !webhook {
		q := url.Values{}
		for _, kv := range c.StringSlice("filter") {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return fmt.Errorf("invalid --filter %q (expected key=value)", kv)
			}
			q.Add(k, v)
		}
		path := "/api/v2/workspaces/" + url.PathEscape(ws) + "/work-items/"
		if projectID != "" {
			path = "/api/v2/workspaces/" + url.PathEscape(ws) + "/projects/" + projectID + "/work-items/"
		}
		p := listen.NewPoller(cl, projects, out, filter, ws, path, q)
		p.Interval = c.Duration("interval")
		p.Activities = c.Bool("activities")
		if s := c.String("since"); s != "" {
			if p.Since, err = parseSince(s); err != nil {
				return err
			}
		}
		fmt.Fprintf(os.Stderr, "listen: poll %s each %s (Ctrl-C to stop)\n", path, p.Interval)
		return p.Run(ctx)
	}

	scopes := filter.Scopes()
	if len(scopes) == 0 {
		return fmt.Errorf("--events %q matches no webhook event", c.String("events"))
	}
	recv := listen.NewReceiver(projects, out, filter)
	recv.ProjectID = projectID
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", c.Int("port")))
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: recv, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	public := c.String("public-url")
	if public == "" {
		fmt.Fprintln(os.Stderr, "listen: start cloudflared tunnel ...")
		t, err := listen.StartTunnel(ctx, "http://"+ln.Addr().String(), 60*time.Second)
		if err != nil {
			return err
		}
		defer t.Close()
		public = t.URL
	}

	if n, err := listen.DeleteStaleHooks(ctx, cl, ws); err == nil && n > 0 {
		fmt.Fprintf(os.Stderr, "listen: deleted %d webhook(s) of earlier runs\n", n)
	}
	hook, err := listen.CreateHook(ctx, cl, ws, public, scopes)
	if err != nil {
		return err
	}
	recv.SetSecret(hook.Secret)
	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := hook.Delete(dctx); err != nil {
			fmt.Fprintf(os.Stderr, "listen: delete webhook %s: %v\n", hook.ID, err)
			return
		}
		fmt.Fprintf(os.Stderr, "listen: deleted webhook %s\n", hook.ID)
	}()
	fmt.Fprintf(os.Stderr, "listen: webhook %s → %s → %s, %d event types (Ctrl-C to stop)\n", hook.ID, public, ln.Addr(), len(scopes))
	<-ctx.Done()
	return nil
}

// parseSince reads an RFC 3339 time or a duration before now.
func parseSince(s string) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid --since %q (use RFC 3339 or a duration like 1h)", s)
	}
	return t, nil
}
