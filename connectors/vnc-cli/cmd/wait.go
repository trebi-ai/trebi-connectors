package cmd

import (
	"context"
	"errors"
	"fmt"
	"image"
	"strconv"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/session"
)

func (r *Runner) expectCommand() *cli.Command {
	return &cli.Command{
		Name:      "expect",
		Usage:     "Wait until the screen matches a picture (see --fuzz, --blur, and --timeout)",
		ArgsUsage: "FILE",
		Flags:     []cli.Flag{&cli.StringFlag{Name: "region", Aliases: []string{"r"}, Usage: "compare the picture at X,Y of the screen"}},
		Action: func(ctx context.Context, c *cli.Command) error {
			if c.NArg() != 1 {
				return usage(fmt.Errorf("expect needs one FILE"))
			}
			at, err := parsePoint(c.String("region"))
			if err != nil {
				return err
			}
			return r.do(ctx, r.expect(c.Args().First(), at, r.match))
		},
	}
}

func (r *Runner) expect(file string, at image.Point, m session.Match) step {
	return func(ctx context.Context, rm remote) error {
		ref, err := loadImage(file)
		if err != nil {
			return err
		}
		if err := rm.Expect(ctx, ref, at, m); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("expect %s: the screen did not match: %w", file, timeoutError{})
			}
			return err
		}
		return nil
	}
}

func (r *Runner) stableCommand() *cli.Command {
	return &cli.Command{
		Name:      "stable",
		Usage:     "Wait until the screen shows no change for SECONDS",
		ArgsUsage: "SECONDS",
		Flags:     []cli.Flag{rectFlag},
		Action: func(ctx context.Context, c *cli.Command) error {
			if c.NArg() != 1 {
				return usage(fmt.Errorf("stable needs SECONDS"))
			}
			d, err := parseSeconds(c.Args().First())
			if err != nil {
				return err
			}
			area, err := parseRect(c.String("region"))
			if err != nil {
				return err
			}
			return r.do(ctx, stable(d, area, r.match))
		},
	}
}

func stable(d time.Duration, area image.Rectangle, m session.Match) step {
	return func(ctx context.Context, rm remote) error {
		if err := rm.Stable(ctx, d, area, m); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("stable: the screen did not stop changing: %w", timeoutError{})
			}
			return err
		}
		return nil
	}
}

func parseSeconds(s string) (time.Duration, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, usage(fmt.Errorf("%q is not a number of seconds", s))
	}
	return time.Duration(v * float64(time.Second)), nil
}
