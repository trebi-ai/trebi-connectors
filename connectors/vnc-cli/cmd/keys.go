package cmd

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/session"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
)

var forceCapsFlag = &cli.BoolFlag{Name: "force-caps", Usage: "hold shift for upper case letters and shifted symbols"}

func (r *Runner) keyCommand() *cli.Command {
	return &cli.Command{
		Name:      "key",
		Usage:     "Press keys or combos such as enter, ctrl-c, or ctrl-alt-del; key down KEY and key up KEY hold and release one key",
		ArgsUsage: "KEY... | down KEY | up KEY",
		Flags:     []cli.Flag{forceCapsFlag},
		Action: func(ctx context.Context, c *cli.Command) error {
			args := c.Args().Slice()
			if len(args) == 0 {
				return usage(fmt.Errorf("key needs a KEY"))
			}
			if len(args) == 2 && (args[0] == "down" || args[0] == "up") {
				s, err := keyHold(args[1], args[0] == "down")
				if err != nil {
					return err
				}
				return r.do(ctx, s)
			}
			steps := make([]step, 0, len(args))
			for _, k := range args {
				s, err := keyPress(k, c.Bool("force-caps"))
				if err != nil {
					return err
				}
				steps = append(steps, s)
			}
			return r.do(ctx, steps...)
		},
	}
}

func input(actions ...session.Action) step {
	return func(ctx context.Context, rm remote) error { return rm.Input(ctx, actions) }
}

// pressActions presses the keysyms in order and releases them in reverse.
func pressActions(keys []uint32) []session.Action {
	out := make([]session.Action, 0, 2*len(keys))
	for _, k := range keys {
		out = append(out, session.Action{Op: session.OpKey, Keysym: k, Down: true})
	}
	for i := len(keys) - 1; i >= 0; i-- {
		out = append(out, session.Action{Op: session.OpKey, Keysym: keys[i]})
	}
	return out
}

func keyPress(key string, forceCaps bool) (step, error) {
	keys, err := vnc.ParseKey(key, forceCaps)
	if err != nil {
		return nil, usage(err)
	}
	return input(pressActions(keys)...), nil
}

func keyHold(key string, down bool) (step, error) {
	keys, err := vnc.ParseKey(key, false)
	if err != nil {
		return nil, usage(err)
	}
	if len(keys) != 1 {
		return nil, usage(fmt.Errorf("key down and key up take one key, not %q", key))
	}
	return input(session.Action{Op: session.OpKey, Keysym: keys[0], Down: down}), nil
}

func (r *Runner) typeCommand() *cli.Command {
	return &cli.Command{
		Name:      "type",
		Usage:     "Type text; - reads it from stdin. Use --env or --file for a secret, so that it is not in the command line",
		ArgsUsage: "TEXT | -",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "env", Usage: "type the value of this env name"},
			&cli.StringFlag{Name: "file", Usage: "type the content of this file"},
			&cli.BoolFlag{Name: "enter", Usage: "press enter after the text"},
			&cli.BoolFlag{Name: "human", Usage: "type with random pauses, as a person does"},
			forceCapsFlag,
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			text, err := r.typedText(c)
			if err != nil {
				return err
			}
			if c.Bool("enter") {
				text += "\n"
			}
			return r.do(ctx, input(typeActions(text, c.Bool("force-caps"), r.keyDelay(c.Bool("human")))...))
		},
	}
}

func (r *Runner) typedText(c *cli.Command) (string, error) {
	sources := 0
	for _, f := range []string{"env", "file"} {
		if c.String(f) != "" {
			sources++
		}
	}
	if c.NArg() > 0 {
		sources++
	}
	if sources != 1 || c.NArg() > 1 {
		return "", usage(fmt.Errorf("type needs one of TEXT, -, --env NAME, or --file PATH"))
	}
	switch {
	case c.String("env") != "":
		v, ok := os.LookupEnv(c.String("env"))
		if !ok {
			return "", usage(fmt.Errorf("--env: %s is not set", c.String("env")))
		}
		return v, nil
	case c.String("file") != "":
		b, err := os.ReadFile(c.String("file"))
		if err != nil {
			return "", usage(err)
		}
		return strings.TrimSuffix(string(b), "\n"), nil
	case c.Args().First() == "-":
		b, err := io.ReadAll(r.stdin)
		if err != nil {
			return "", err
		}
		return strings.TrimSuffix(string(b), "\n"), nil
	}
	return c.Args().First(), nil
}

// keyDelay is the pause before each typed key. --human types from 40 to
// 180ms per key, and sometimes waits longer after a word.
func (r *Runner) keyDelay(human bool) func(prev rune) time.Duration {
	if !human {
		return func(rune) time.Duration { return r.delay.pick() }
	}
	return func(prev rune) time.Duration {
		d := 40*time.Millisecond + rand.N(140*time.Millisecond)
		if (prev == ' ' || prev == '.' || prev == ',') && rand.N(4) == 0 {
			d += 200*time.Millisecond + rand.N(400*time.Millisecond)
		}
		return d
	}
}

// typeActions types each character as one key press. \r is dropped,
// because \n already presses enter.
func typeActions(text string, forceCaps bool, delay func(prev rune) time.Duration) []session.Action {
	var out []session.Action
	prev := rune(-1)
	for _, ch := range text {
		if ch == '\r' {
			continue
		}
		if prev >= 0 {
			if d := delay(prev); d > 0 {
				out = append(out, session.Action{Op: session.OpWait, Wait: d})
			}
		}
		keys, _ := vnc.ParseKey(string(ch), forceCaps)
		out = append(out, pressActions(keys)...)
		prev = ch
	}
	return out
}
