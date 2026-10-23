package cmd

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/session"
)

func (r *Runner) runCommand() *cli.Command {
	return &cli.Command{
		Name:  "run",
		Usage: "Play a vncdotool script (.vdo), or a chain of commands after --, on one connection",
		Description: "Commands: key KEY, keydown KEY, keyup KEY, type TEXT, typefile FILE, pastefile FILE, move X Y, click BUTTON, " +
			"mousedown BUTTON, mouseup BUTTON, drag X Y, capture FILE, rcapture FILE X Y W H, expect FILE [FUZZ], " +
			"rexpect FILE X Y [FUZZ], stable SECONDS [FUZZ], rstable SECONDS X Y W H [FUZZ], pause SECONDS, and a script file name.\n\n" +
			"Example: vnc-cli run -- move 10 10 click 1 type hello key enter",
		ArgsUsage: "FILE | - | -- COMMAND...",
		Action: func(ctx context.Context, c *cli.Command) error {
			args := c.Args().Slice()
			if len(args) == 0 {
				return usage(errors.New("run needs a FILE, -, or -- and commands"))
			}
			if len(args) == 1 && args[0] == "-" {
				b, err := io.ReadAll(r.stdin)
				if err != nil {
					return err
				}
				if args, err = splitScript(string(b)); err != nil {
					return usage(err)
				}
			}
			steps, err := r.parseScript(args)
			if err != nil {
				return err
			}
			return r.do(ctx, steps...)
		},
	}
}

// maxScriptDepth limits scripts that name other scripts.
const maxScriptDepth = 8

// parseScript reads vncdotool commands (vncdotool command.py
// build_command_list).
func (r *Runner) parseScript(args []string) ([]step, error) {
	p := &scriptParser{r: r, args: args}
	if err := p.parse(); err != nil {
		return nil, usage(err)
	}
	return p.steps, nil
}

type scriptParser struct {
	r     *Runner
	args  []string
	steps []step
	depth int
}

func (p *scriptParser) next(cmd string) (string, error) {
	if len(p.args) == 0 {
		return "", fmt.Errorf("%s: an argument is missing", cmd)
	}
	a := p.args[0]
	p.args = p.args[1:]
	return a, nil
}

func (p *scriptParser) ints(cmd string, n int) ([]int, error) {
	out := make([]int, n)
	for i := range out {
		a, err := p.next(cmd)
		if err != nil {
			return nil, err
		}
		if out[i], err = strconv.Atoi(a); err != nil {
			return nil, fmt.Errorf("%s: %q is not a number", cmd, a)
		}
	}
	return out, nil
}

// fuzz reads an optional trailing fuzz (vncdotool _trailing_fuzz).
func (p *scriptParser) fuzz(cmd string) (session.Match, error) {
	m := p.r.match
	if len(p.args) == 0 {
		return m, nil
	}
	v, err := strconv.ParseFloat(p.args[0], 64)
	if err != nil {
		return m, nil
	}
	if v != float64(int(v)) || v < 0 || v > 255 {
		return m, fmt.Errorf("%s takes a whole-number fuzz from 0 (exact) to 255, not %s", cmd, p.args[0])
	}
	p.args = p.args[1:]
	m.Fuzz = int(v)
	return m, nil
}

func (p *scriptParser) add(s step) { p.steps = append(p.steps, s) }

func (p *scriptParser) parse() error {
	for len(p.args) > 0 {
		cmd, _ := p.next("")
		if err := p.command(cmd); err != nil {
			return err
		}
	}
	return nil
}

func (p *scriptParser) command(cmd string) error {
	r := p.r
	switch cmd {
	case "key", "keydown", "kdown", "keyup", "kup":
		k, err := p.next(cmd)
		if err != nil {
			return err
		}
		var s step
		switch cmd {
		case "key":
			s, err = keyPress(k, false)
		default:
			s, err = keyHold(k, cmd == "keydown" || cmd == "kdown")
		}
		if err != nil {
			return err
		}
		p.add(s)
	case "move", "mousemove", "drag":
		n, err := p.ints(cmd, 2)
		if err != nil {
			return err
		}
		if cmd == "drag" {
			p.add(dragStep(n[0], n[1], 5))
		} else {
			p.add(move(n[0], n[1]))
		}
	case "click", "mousedown", "mdown", "mouseup", "mup":
		n, err := p.ints(cmd, 1)
		if err != nil {
			return err
		}
		mask, err := buttonMask(n[0])
		if err != nil {
			return err
		}
		switch cmd {
		case "click":
			p.add(click(mask))
		default:
			p.add(button(mask, cmd == "mousedown" || cmd == "mdown"))
		}
	case "type", "typefile":
		a, err := p.next(cmd)
		if err != nil {
			return err
		}
		text := a
		if cmd == "typefile" {
			if text, err = readArg(r.stdin, a); err != nil {
				return err
			}
		}
		p.add(input(typeActions(text, false, func(rune) time.Duration { return r.delay.pick() })...))
	case "pastefile":
		a, err := p.next(cmd)
		if err != nil {
			return err
		}
		text, err := readArg(r.stdin, a)
		if err != nil {
			return err
		}
		p.add(paste(strings.ReplaceAll(text, "\r\n", "\n")))
	case "capture", "rcapture":
		file, err := p.next(cmd)
		if err != nil {
			return err
		}
		if err := checkFormat(file); err != nil {
			return err
		}
		var area image.Rectangle
		if cmd == "rcapture" {
			n, err := p.ints(cmd, 4)
			if err != nil {
				return err
			}
			area = image.Rect(n[0], n[1], n[0]+n[2], n[1]+n[3])
		}
		p.add(r.capture(file, area))
	case "expect", "rexpect":
		file, err := p.next(cmd)
		if err != nil {
			return err
		}
		var at image.Point
		if cmd == "rexpect" {
			n, err := p.ints(cmd, 2)
			if err != nil {
				return err
			}
			at = image.Pt(n[0], n[1])
		}
		m, err := p.fuzz(cmd)
		if err != nil {
			return err
		}
		p.add(r.expect(file, at, m))
	case "stable", "rstable":
		a, err := p.next(cmd)
		if err != nil {
			return err
		}
		d, err := parseSeconds(a)
		if err != nil {
			return err
		}
		var area image.Rectangle
		if cmd == "rstable" {
			n, err := p.ints(cmd, 4)
			if err != nil {
				return err
			}
			area = image.Rect(n[0], n[1], n[0]+n[2], n[1]+n[3])
		}
		m, err := p.fuzz(cmd)
		if err != nil {
			return err
		}
		p.add(stable(d, area, m))
	case "pause", "sleep":
		a, err := p.next(cmd)
		if err != nil {
			return err
		}
		d, err := parseSeconds(a)
		if err != nil {
			return err
		}
		p.add(func(ctx context.Context, _ remote) error { return sleep(ctx, d) })
	default:
		return p.include(cmd)
	}
	return nil
}

// include puts the commands of a script file in place of its name.
func (p *scriptParser) include(file string) error {
	st, err := os.Stat(file)
	if err != nil || st.IsDir() {
		return fmt.Errorf("unknown command %q", file)
	}
	if p.depth >= maxScriptDepth {
		return fmt.Errorf("%s: scripts name each other more than %d times", file, maxScriptDepth)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	toks, err := splitScript(string(b))
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	p.depth++
	p.args = append(toks, p.args...)
	return nil
}

func readArg(stdin io.Reader, file string) (string, error) {
	var b []byte
	var err error
	if file == "-" {
		b, err = io.ReadAll(stdin)
	} else {
		b, err = os.ReadFile(file)
	}
	return string(b), err
}

// splitScript splits a script into words as Python shlex in POSIX mode
// with whitespace_split: quotes, backslash escapes, and # comments.
func splitScript(s string) ([]string, error) {
	var (
		out   []string
		cur   strings.Builder
		inTok bool
		quote rune
	)
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		ch := rs[i]
		switch {
		case quote == '\'':
			if ch == '\'' {
				quote = 0
			} else {
				cur.WriteRune(ch)
			}
		case quote == '"':
			switch {
			case ch == '"':
				quote = 0
			case ch == '\\' && i+1 < len(rs) && strings.ContainsRune("\"\\", rs[i+1]):
				i++
				cur.WriteRune(rs[i])
			default:
				cur.WriteRune(ch)
			}
		case ch == '\'' || ch == '"':
			quote, inTok = ch, true
		case ch == '\\':
			if i+1 < len(rs) {
				i++
				if rs[i] != '\n' {
					cur.WriteRune(rs[i])
				}
			}
			inTok = true
		case ch == '#' && !inTok:
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			if inTok {
				out = append(out, cur.String())
				cur.Reset()
				inTok = false
			}
		default:
			cur.WriteRune(ch)
			inTok = true
		}
	}
	if quote != 0 {
		return nil, errors.New("a quote is not closed")
	}
	if inTok {
		out = append(out, cur.String())
	}
	return out, nil
}
