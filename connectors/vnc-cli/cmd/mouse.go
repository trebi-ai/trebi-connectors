package cmd

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/session"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
)

// Mouse buttons by number, as X11 and vncdotool count them: 1 left,
// 2 middle, 3 right, 4 and 5 wheel up and down, 6 and 7 wheel left and
// right.
func buttonMask(n int) (uint8, error) {
	if n < 1 || n > 8 {
		return 0, usage(fmt.Errorf("button %d is not 1 to 8", n))
	}
	return 1 << (n - 1), nil
}

func parseButton(s string) (uint8, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, usage(fmt.Errorf("button %q is not a number", s))
	}
	return buttonMask(n)
}

func parseXY(args []string) (int, int, error) {
	if len(args) != 2 {
		return 0, 0, usage(fmt.Errorf("want X Y"))
	}
	x, err1 := strconv.Atoi(args[0])
	y, err2 := strconv.Atoi(args[1])
	if err1 != nil || err2 != nil || x < 0 || y < 0 {
		return 0, 0, usage(fmt.Errorf("%s %s is not X Y", args[0], args[1]))
	}
	return x, y, nil
}

func move(x, y int) step { return input(session.Action{Op: session.OpMove, X: x, Y: y}) }

func click(mask uint8) step {
	return input(
		session.Action{Op: session.OpButton, Button: mask, Down: true},
		session.Action{Op: session.OpButton, Button: mask},
	)
}

func button(mask uint8, down bool) step {
	return input(session.Action{Op: session.OpButton, Button: mask, Down: down})
}

// dragStep moves in a line of steps of n pixels. vncdotool drag holds no
// button; the drag command adds one.
func dragStep(x, y, n int) step {
	return input(session.Action{Op: session.OpMove, X: x, Y: y, Step: max(n, 1), Wait: 10 * time.Millisecond})
}

func (r *Runner) moveCommand() *cli.Command {
	return &cli.Command{
		Name:      "move",
		Usage:     "Move the pointer to X Y",
		ArgsUsage: "X Y",
		Action: func(ctx context.Context, c *cli.Command) error {
			x, y, err := parseXY(c.Args().Slice())
			if err != nil {
				return err
			}
			return r.do(ctx, move(x, y))
		},
	}
}

func (r *Runner) clickCommand() *cli.Command {
	return &cli.Command{
		Name:      "click",
		Usage:     "Click a button at the pointer: 1 left (default), 2 middle, 3 right",
		ArgsUsage: "[BUTTON]",
		Action: func(ctx context.Context, c *cli.Command) error {
			b := "1"
			switch c.NArg() {
			case 0:
			case 1:
				b = c.Args().First()
			default:
				return usage(fmt.Errorf("click takes one BUTTON"))
			}
			mask, err := parseButton(b)
			if err != nil {
				return err
			}
			return r.do(ctx, click(mask))
		},
	}
}

func (r *Runner) mouseCommand() *cli.Command {
	return &cli.Command{
		Name:      "mouse",
		Usage:     "Hold (down) or release (up) a button at the pointer",
		ArgsUsage: "down|up BUTTON",
		Action: func(ctx context.Context, c *cli.Command) error {
			args := c.Args().Slice()
			if len(args) != 2 || (args[0] != "down" && args[0] != "up") {
				return usage(fmt.Errorf("want mouse down BUTTON or mouse up BUTTON"))
			}
			mask, err := parseButton(args[1])
			if err != nil {
				return err
			}
			return r.do(ctx, button(mask, args[0] == "down"))
		},
	}
}

func (r *Runner) dragCommand() *cli.Command {
	return &cli.Command{
		Name:      "drag",
		Usage:     "Hold a button, move the pointer to X Y in steps, and release the button",
		ArgsUsage: "X Y",
		Flags: []cli.Flag{
			&cli.IntFlag{Name: "step", Value: 5, Usage: "pixels per step"},
			&cli.IntFlag{Name: "button", Value: 1, Usage: "the button to hold (0: none)"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			x, y, err := parseXY(c.Args().Slice())
			if err != nil {
				return err
			}
			if c.Int("button") == 0 {
				return r.do(ctx, dragStep(x, y, c.Int("step")))
			}
			mask, err := buttonMask(c.Int("button"))
			if err != nil {
				return err
			}
			return r.do(ctx, button(mask, true), dragStep(x, y, c.Int("step")), button(mask, false))
		},
	}
}

func (r *Runner) scrollCommand() *cli.Command {
	return &cli.Command{
		Name:            "scroll",
		Usage:           "Turn the wheel: DY > 0 scrolls down, DX > 0 scrolls right",
		ArgsUsage:       "DX DY",
		SkipFlagParsing: true,
		Action: func(ctx context.Context, c *cli.Command) error {
			args := c.Args().Slice()
			if len(args) != 2 {
				return usage(fmt.Errorf("want scroll DX DY"))
			}
			dx, err1 := strconv.Atoi(args[0])
			dy, err2 := strconv.Atoi(args[1])
			if err1 != nil || err2 != nil {
				return usage(fmt.Errorf("%s %s is not DX DY", args[0], args[1]))
			}
			return r.do(ctx, scroll(dx, dy))
		},
	}
}

func scroll(dx, dy int) step {
	var acts []session.Action
	add := func(n int, neg, pos uint8) {
		mask := pos
		if n < 0 {
			mask, n = neg, -n
		}
		for range n {
			acts = append(acts,
				session.Action{Op: session.OpButton, Button: mask, Down: true},
				session.Action{Op: session.OpButton, Button: mask})
		}
	}
	add(dy, vnc.ButtonWheelUp, vnc.ButtonWheelDown)
	add(dx, vnc.ButtonWheelLeft, vnc.ButtonWheelRight)
	return input(acts...)
}
