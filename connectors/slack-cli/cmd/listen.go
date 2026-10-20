package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	cli "github.com/urfave/cli/v3"

	"github.com/flarco/cli-tools/slack-cli/internal/client"
	"github.com/flarco/cli-tools/slack-cli/internal/config"
	"github.com/flarco/cli-tools/slack-cli/internal/listen"
)

// ListenCommand streams live events as JSON lines.
func ListenCommand() *cli.Command {
	return &cli.Command{
		Name:  "listen",
		Usage: "Stream live events as JSON lines",
		Description: `Streams Slack events and prints one JSON object per line.

Transport is chosen per workspace from the token:
  session (xoxc-)  the desktop client WebSocket (all events the user sees)
  app (xapp-)      Socket Mode (needs a Socket Mode app; events it subscribes to)
  bot/user OAuth   polls conversations.history (needs --channel filters)

Filter with --channel, --user, --type, --thread, --mentions, --dm, and
--no-self. Channel and user names resolve to ids. Use --all-workspaces to
merge every configured workspace. Stop with Ctrl-C, --count, or --timeout.`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{Name: "channel", Aliases: []string{"c"}, Usage: "Only these channels (id, #name, @user; repeatable)"},
			&cli.StringSliceFlag{Name: "user", Aliases: []string{"u"}, Usage: "Only these users (id, @name, email; repeatable)"},
			&cli.StringSliceFlag{Name: "type", Aliases: []string{"t"}, Usage: "Only these event types (e.g. message; repeatable)"},
			&cli.StringFlag{Name: "thread", Usage: "Only this thread (ts)"},
			&cli.BoolFlag{Name: "mentions", Usage: "Only messages that mention you"},
			&cli.BoolFlag{Name: "dm", Usage: "Only direct messages"},
			&cli.BoolFlag{Name: "no-self", Usage: "Drop events you authored"},
			&cli.BoolFlag{Name: "all-workspaces", Aliases: []string{"A"}, Usage: "Listen on every configured workspace"},
			&cli.IntFlag{Name: "count", Aliases: []string{"n"}, Usage: "Stop after this many events"},
			&cli.DurationFlag{Name: "timeout", Usage: "Stop after this duration (e.g. 30s)"},
			&cli.DurationFlag{Name: "poll-interval", Value: 5 * time.Second, Usage: "Poll period for OAuth tokens"},
			&cli.BoolFlag{Name: "eager", Usage: "Session socket: subscribe to every channel (no lazy_channels)"},
			&cli.BoolFlag{Name: "raw", Usage: "Emit the raw event object, not the normalized one"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			names, err := listenWorkspaces(a, c.Bool("all-workspaces"))
			if err != nil {
				return err
			}

			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			if d := c.Duration("timeout"); d > 0 {
				ctx, cancel = context.WithTimeout(ctx, d)
				defer cancel()
			}

			events := make(chan listen.Event, 256)
			errs := func(name string, err error) {
				if a.Verbose {
					fmt.Fprintf(os.Stderr, "[%s] %v; reconnecting\n", name, err)
				}
			}

			started := 0
			for _, name := range names {
				w := a.Config.Workspaces[name]
				src, ferr := buildSource(ctx, a, name, w, c)
				if ferr != nil {
					if len(names) == 1 {
						return ferr
					}
					fmt.Fprintf(os.Stderr, "[%s] skipped: %v\n", name, ferr)
					continue
				}
				started++
				go func() {
					_ = listen.Stream(ctx, src, events, errs)
				}()
			}
			if started == 0 {
				return fmt.Errorf("no workspace could start a stream")
			}

			return emit(ctx, c, events)
		},
	}
}

// listenWorkspaces returns the workspace names to stream.
func listenWorkspaces(a *AppContext, all bool) ([]string, error) {
	if all {
		names := a.Config.Names()
		if len(names) == 0 {
			return nil, fmt.Errorf("no workspace configured")
		}
		return names, nil
	}
	name, _, err := a.Workspace()
	if err != nil {
		return nil, err
	}
	return []string{name}, nil
}

// buildSource makes the right source for a workspace and its filter.
func buildSource(ctx context.Context, a *AppContext, name string, w config.Workspace, c *cli.Command) (listen.Source, error) {
	cl := client.New(w)
	cl.Verbose = a.Verbose
	dir, _ := config.Dir()
	directory := client.NewDirectory(cl, dir, name)

	filter, channels, err := buildFilter(ctx, a, w, directory, c)
	if err != nil {
		return nil, err
	}

	switch w.Kind() {
	case "session":
		if w.TeamID == "" {
			return nil, fmt.Errorf("session token has no team id; run `slack-cli auth import` again")
		}
		return &filtered{Source: &listen.WSSource{Workspace: name, TeamID: w.TeamID, Client: cl, Eager: c.Bool("eager")}, filter: filter}, nil
	case "app":
		return &filtered{Source: &listen.SocketSource{Workspace: name, TeamID: w.TeamID, AppClient: cl}, filter: filter}, nil
	default:
		if len(channels) == 0 {
			return nil, fmt.Errorf("a %s token needs --channel to poll (it cannot open a socket)", w.Kind())
		}
		return &filtered{
			Source: &listen.PollSource{Workspace: name, TeamID: w.TeamID, Client: cl, Channels: channels, Interval: c.Duration("poll-interval")},
			filter: filter,
		}, nil
	}
}

// buildFilter resolves name filters to ids and returns the filter plus the
// resolved channel ids (for poll mode).
func buildFilter(ctx context.Context, a *AppContext, w config.Workspace, dir *client.Directory, c *cli.Command) (listen.Filter, []string, error) {
	f := listen.Filter{
		Types:    toSet(c.StringSlice("type")),
		Thread:   c.String("thread"),
		SelfID:   w.UserID,
		Mentions: c.Bool("mentions"),
		DMs:      c.Bool("dm"),
		NoSelf:   c.Bool("no-self"),
	}
	var channels []string
	if refs := c.StringSlice("channel"); len(refs) > 0 {
		f.Channels = map[string]bool{}
		for _, ref := range refs {
			id := ref
			if !a.NoResolve && !client.IsChannelID(ref) {
				var err error
				if id, err = dir.ResolveChannel(ctx, ref); err != nil {
					return f, nil, fmt.Errorf("channel %q: %w", ref, err)
				}
			}
			f.Channels[id] = true
			channels = append(channels, id)
		}
	}
	if refs := c.StringSlice("user"); len(refs) > 0 {
		f.Users = map[string]bool{}
		for _, ref := range refs {
			id := ref
			if !a.NoResolve && !client.IsUserID(ref) {
				var err error
				if id, err = dir.ResolveUser(ctx, ref); err != nil {
					return f, nil, fmt.Errorf("user %q: %w", ref, err)
				}
			}
			f.Users[id] = true
		}
	}
	return f, channels, nil
}

// emit prints events as JSON lines until the context ends or count is reached.
func emit(ctx context.Context, c *cli.Command, events <-chan listen.Event) error {
	raw := c.Bool("raw")
	limit := c.Int("count")
	enc := json.NewEncoder(c.Root().Writer)
	n := 0
	for {
		select {
		case <-ctx.Done():
			if err := ctx.Err(); err == context.Canceled || err == context.DeadlineExceeded {
				return nil
			}
			return ctx.Err()
		case e := <-events:
			var err error
			if raw {
				err = enc.Encode(json.RawMessage(e.Raw))
			} else {
				err = enc.Encode(e)
			}
			if err != nil {
				return err
			}
			if n++; limit > 0 && n >= limit {
				return nil
			}
		}
	}
}

func toSet(vs []string) map[string]bool {
	if len(vs) == 0 {
		return nil
	}
	m := make(map[string]bool, len(vs))
	for _, v := range vs {
		m[strings.TrimSpace(v)] = true
	}
	return m
}

// filtered wraps a source and drops events that do not pass the filter.
type filtered struct {
	listen.Source
	filter listen.Filter
}

func (f *filtered) Run(ctx context.Context, out chan<- listen.Event) error {
	inner := make(chan listen.Event, 64)
	done := make(chan error, 1)
	go func() { done <- f.Source.Run(ctx, inner) }()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			return err
		case e := <-inner:
			if f.filter.Match(e) {
				select {
				case out <- e:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
}
