package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/session"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/watch"
)

var errWatchDone = errors.New("watch done")

// watcher streams the events of a watch.
type watcher interface {
	Watch(ctx context.Context, spec watch.Spec, emit func(watch.Event) error) error
}

func (r *Runner) watchCommand() *cli.Command {
	return &cli.Command{
		Name:  "watch",
		Usage: "Watch areas of the screen and write a JSON line for each change (the same events as glkvm-cli watch)",
		// Areas contain commas.
		DisableSliceFlagSeparator: true,
		Description: `The server sends the changed areas of the screen, so watch reads only the
areas that changed. Events go to stdout, one JSON object per line:

  ready         the areas in reference pixels, the reference size, and the screen
  change        an area starts to change (changed_pct, changed_px, bbox of the change)
  stable        the area has not changed for --settle (changing_ms, bbox against the
                state before the change); this state is the new base
  error         the watch failed
  clipboard     the remote clipboard changed (text)
  bell          the remote computer rang the bell
  resize        the screen size changed (size); ready follows with the new size
  cursor        the pointer shape changed (cursor); not sent unless --events has it
  disconnected  the connection dropped (error); the session connects again
  connected     the connection is back; ready follows

Area coordinates are pixels of a reference image of size --ref (default: the
screen). bbox uses the same coordinates. Without --area, the whole screen is one
area named "screen".`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{Name: "area", Aliases: []string{"a"}, Usage: "an area to watch, [NAME=]X,Y,W,H in reference pixels (repeat for more)"},
			&cli.StringFlag{Name: "ref", Usage: "the reference image size, W or WxH (default: the screen)"},
			&cli.FloatFlag{Name: "threshold", Value: 0.5, Usage: "percent of the area pixels that must change"},
			&cli.IntFlag{Name: "min-pixels", Value: 16, Usage: "the fewest changed pixels for a change"},
			&cli.IntFlag{Name: "pixel-delta", Value: 24, Usage: "the brightness difference (0-255) that counts as a changed pixel"},
			&cli.DurationFlag{Name: "settle", Value: 700 * time.Millisecond, Usage: "no change for this long ends a change"},
			&cli.FloatFlag{Name: "max-fps", Value: 10, Usage: "the most checks per second"},
			&cli.StringFlag{Name: "events", Usage: "the events to write, comma separated, from " + strings.Join(watch.AllEvents, ",") + " (default: all but cursor)"},
			&cli.StringFlag{Name: "save", Usage: "a folder for a PNG of the area with each change and stable event"},
			&cli.BoolFlag{Name: "once", Usage: "exit after the first stable event (with -d: exit 1 when none comes in time)"},
			&cli.DurationFlag{Name: "duration", Aliases: []string{"d"}, Usage: "exit after this time (default: until Ctrl-C)"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			spec, err := watchSpec(c)
			if err != nil {
				return usage(err)
			}
			if d := c.Duration("duration"); d > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, d)
				defer cancel()
			}
			ctx, stop := context.WithCancelCause(ctx)
			defer stop(nil)
			w, err := r.watcher(ctx, stop)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(r.stdout)
			once := c.Bool("once")
			err = w.Watch(ctx, spec, func(ev watch.Event) error {
				if err := enc.Encode(ev); err != nil {
					return err
				}
				if once && ev.Event == watch.KindStable {
					return errWatchDone
				}
				return nil
			})
			switch cause := context.Cause(ctx); {
			case errors.Is(err, errWatchDone):
				return nil
			case once && errors.Is(cause, context.DeadlineExceeded):
				return fmt.Errorf("no stable event within %s", c.Duration("duration"))
			case errors.Is(cause, context.DeadlineExceeded):
				return nil
			case cause != nil && !errors.Is(cause, context.Canceled):
				return cause
			}
			return err
		},
	}
}

func watchSpec(c *cli.Command) (watch.Spec, error) {
	spec := watch.Spec{Options: watch.Options{
		Threshold:  c.Float("threshold"),
		MinPixels:  int(c.Int("min-pixels")),
		PixelDelta: int(c.Int("pixel-delta")),
		Settle:     c.Duration("settle"),
		MaxFPS:     c.Float("max-fps"),
	}}
	for i, s := range c.StringSlice("area") {
		a, err := watch.ParseArea(s, i+1)
		if err != nil {
			return spec, err
		}
		spec.Areas = append(spec.Areas, a)
	}
	if s := c.String("ref"); s != "" {
		var err error
		if spec.Ref, err = watch.ParseSize(s); err != nil {
			return spec, err
		}
	}
	if s := c.String("events"); s != "" {
		for _, k := range strings.Split(s, ",") {
			k = strings.TrimSpace(k)
			if !slices.Contains(watch.AllEvents, k) {
				return spec, fmt.Errorf("--events: %q is not one of %s", k, strings.Join(watch.AllEvents, ", "))
			}
			spec.Events = append(spec.Events, k)
		}
	}
	if s := c.String("save"); s != "" {
		abs, err := filepath.Abs(s)
		if err != nil {
			return spec, err
		}
		spec.Options.SaveDir = abs
	}
	if spec.Options.MaxFPS < 0 || spec.Options.Threshold < 0 || spec.Options.Settle < 0 {
		return spec, errors.New("--max-fps, --threshold, and --settle must not be negative")
	}
	return spec, nil
}

// watcher is the session with --session auto, else a connection of this
// process that connects again when it drops. A failed login calls stop.
func (r *Runner) watcher(ctx context.Context, stop context.CancelCauseFunc) (watcher, error) {
	if r.session != "off" {
		rm, err := r.remote(ctx)
		if err != nil {
			return nil, err
		}
		if w, ok := rm.(watcher); ok {
			return w, nil
		}
	}
	if m := r.settings.Missing(); m != nil {
		return nil, usage(fmt.Errorf("no server: use --server, --target, or %s", config.EnvServer))
	}
	level := slog.LevelWarn
	if r.verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(r.stderr, &slog.HandlerOptions{Level: level}))
	var h *session.Holder
	_, err := r.withPassword(func(pw string) (remote, error) {
		h = session.NewHolder(r.dialer(pw), r.settings.Server, r.settings.User, 0, log)
		return nil, h.Connect(ctx)
	})
	if err != nil {
		return nil, err
	}
	go h.Keep(ctx, stop)
	return h, nil
}
