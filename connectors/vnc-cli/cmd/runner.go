// Package cmd has the commands of vnc-cli.
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/session"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
)

// remote is a connection to one screen: in this process or in a holder.
type remote interface {
	Info(ctx context.Context) (session.Info, error)
	Capture(ctx context.Context, r image.Rectangle) (*image.RGBA, error)
	Input(ctx context.Context, actions []session.Action) error
	Paste(ctx context.Context, text string) error
	Clipboard(ctx context.Context) (string, error)
	Expect(ctx context.Context, ref image.Image, at image.Point, m session.Match) error
	Stable(ctx context.Context, d time.Duration, r image.Rectangle, m session.Match) error
	Close() error
}

// step is one command on a remote.
type step func(ctx context.Context, rm remote) error

// Runner holds the global flags and the connection of one invocation.
type Runner struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	// prompt reads a password from the terminal. Nil when stdin is not a
	// terminal.
	prompt func(label string) (string, error)

	settings config.Settings
	opts     vnc.Options
	// connArgs are the flags that shape the connection. A holder gets
	// them, and they are part of its key.
	connArgs []string
	session  string
	verbose  bool
	timeout  time.Duration
	delay    delayRange
	match    session.Match
	json     bool

	rm remote
}

// NewRunner makes a runner over the standard streams.
func NewRunner(stdin io.Reader, stdout, stderr io.Writer) *Runner {
	r := &Runner{stdin: stdin, stdout: stdout, stderr: stderr}
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		r.prompt = func(label string) (string, error) {
			fmt.Fprintf(stderr, "%s: ", label)
			b, err := term.ReadPassword(int(f.Fd()))
			fmt.Fprintln(stderr)
			return string(b), err
		}
	}
	return r
}

// App is the root command.
func (r *Runner) App(version string) *cli.Command {
	return &cli.Command{
		Name:    "vnc-cli",
		Usage:   "Control a remote screen over VNC: capture, type, click, wait, and watch",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "server", Aliases: []string{"s"}, Usage: "the server: HOST, HOST:DISPLAY, HOST::PORT, a socket path, or ws:// (default: " + config.EnvServer + ")"},
			&cli.StringFlag{Name: "target", Aliases: []string{"t"}, Usage: "a named server from ~/.cli-tools/vnc-cli/config.toml"},
			&cli.StringFlag{Name: "password-env", Aliases: []string{"p"}, Usage: "the env name that holds the password (default: " + config.EnvPassword + ")"},
			&cli.StringFlag{Name: "user", Usage: "the user name for an account login (default: " + config.EnvUser + ")"},
			&cli.DurationFlag{Name: "timeout", Usage: "stop with exit code 40 after this time (0: no limit)"},
			&cli.StringFlag{Name: "delay", Usage: "milliseconds between actions and typed keys, as N or MIN-MAX"},
			&cli.StringFlag{Name: "encodings", Usage: "the encodings to ask for, in order, for example tight,zrle,raw"},
			&cli.IntFlag{Name: "jpeg-quality", Value: -1, Usage: "allow lossy JPEG at this quality, 0 to 9 (-1: lossless)"},
			&cli.StringFlag{Name: "tls-ca-cert", Usage: "trust the server certificates that this CA file signs (VeNCrypt and wss://)"},
			&cli.BoolFlag{Name: "tls-insecure-skip-verify", Usage: "do not check the server certificate"},
			&cli.StringFlag{Name: "pixel-format", Usage: "ask for this pixel format: bgrx8888, rgbx8888, or rgb565"},
			&cli.IntFlag{Name: "fuzz", Value: -1, DefaultText: "from the pixel format", Usage: "expect and stable: the largest color difference of a pixel, 0 (exact) to 255"},
			&cli.IntFlag{Name: "blur", Value: -1, DefaultText: "2 with --jpeg-quality, else 0", Usage: "expect and stable: blur both pictures with this radius first"},
			&cli.StringFlag{Name: "session", Value: "auto", Usage: "auto uses a background session, off connects for this command only"},
			&cli.StringFlag{Name: "idle-timeout", Usage: "a background session stops after this time with no command (default: 5m, " + config.EnvIdleTimeout + ")"},
			&cli.BoolFlag{Name: "json", Aliases: []string{"j"}, Usage: "write JSON"},
			&cli.BoolFlag{Name: "verbose", Aliases: []string{"v"}, Usage: "write protocol logs to stderr"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			if c.NArg() > 0 {
				return usage(fmt.Errorf("unknown command %q", c.Args().First()))
			}
			return cli.ShowRootCommandHelp(c)
		},
		Before:         r.before,
		After:          func(context.Context, *cli.Command) error { return r.close() },
		OnUsageError:   onUsageError,
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
		Commands: []*cli.Command{
			r.captureCommand(), r.infoCommand(),
			r.keyCommand(), r.typeCommand(),
			r.moveCommand(), r.clickCommand(), r.mouseCommand(), r.dragCommand(), r.scrollCommand(),
			r.pasteCommand(), r.clipboardCommand(),
			r.expectCommand(), r.stableCommand(),
			r.runCommand(), r.sessionCommand(), r.configCommand(),
		},
	}
}

func (r *Runner) before(ctx context.Context, c *cli.Command) (context.Context, error) {
	var err error
	r.settings, err = config.Resolve(config.Flags{
		Server: c.String("server"), Target: c.String("target"), User: c.String("user"),
		PasswordEnv: c.String("password-env"), IdleTimeout: c.String("idle-timeout"),
	})
	if err != nil {
		return ctx, usage(err)
	}
	r.timeout = c.Duration("timeout")
	r.json = c.Bool("json")
	if r.delay, err = parseDelay(c.String("delay")); err != nil {
		return ctx, usage(err)
	}
	switch r.session = c.String("session"); r.session {
	case "auto", "off":
	default:
		return ctx, usage(fmt.Errorf("--session: %q is not auto or off", r.session))
	}
	r.opts = vnc.Options{Addr: r.settings.Server, User: r.settings.User}
	r.connArgs = nil
	for _, name := range []string{"encodings", "jpeg-quality", "pixel-format", "tls-ca-cert", "tls-insecure-skip-verify"} {
		if !c.IsSet(name) {
			continue
		}
		v := c.Value(name)
		if name == "tls-ca-cert" {
			if v, err = filepath.Abs(c.String(name)); err != nil {
				return ctx, usage(err)
			}
		}
		r.connArgs = append(r.connArgs, fmt.Sprintf("--%s=%v", name, v))
	}
	if s := c.String("encodings"); s != "" {
		for _, name := range strings.Split(s, ",") {
			e, ok := vnc.EncodingNames[strings.TrimSpace(strings.ToLower(name))]
			if !ok {
				return ctx, usage(fmt.Errorf("--encodings: %q is not a known encoding", name))
			}
			r.opts.Encodings = append(r.opts.Encodings, e)
		}
	}
	if q := c.Int("jpeg-quality"); q >= 0 {
		if q > 9 {
			return ctx, usage(fmt.Errorf("--jpeg-quality: %d is not 0 to 9", q))
		}
		r.opts.JPEG, r.opts.JPEGQuality = true, q
	}
	if name := c.String("pixel-format"); name != "" {
		pf, ok := vnc.PixelFormats[name]
		if !ok {
			return ctx, usage(fmt.Errorf("--pixel-format: %q is not bgrx8888, rgbx8888, or rgb565", name))
		}
		r.opts.PixelFormat = &pf
	}
	if ca, skip := c.String("tls-ca-cert"), c.Bool("tls-insecure-skip-verify"); ca != "" || skip {
		if r.opts.TLS, err = vnc.NewTLSConfig(ca, skip); err != nil {
			return ctx, usage(fmt.Errorf("--tls-ca-cert: %w", err))
		}
	}
	r.match = session.Match{Fuzz: c.Int("fuzz"), Blur: c.Int("blur")}
	if r.match.Fuzz > 255 || r.match.Fuzz < -1 {
		return ctx, usage(fmt.Errorf("--fuzz: %d is not 0 to 255", r.match.Fuzz))
	}
	if r.match.Blur < 0 {
		r.match.Blur = 0
		if r.opts.JPEG {
			r.match.Blur = 2
		}
	}
	if r.verbose = c.Bool("verbose"); r.verbose {
		r.opts.Logger = slog.New(slog.NewTextHandler(r.stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return ctx, nil
}

// remote connects on the first call: to a holder with --session auto, else
// directly.
func (r *Runner) remote(ctx context.Context) (remote, error) {
	if r.rm != nil {
		return r.rm, nil
	}
	var err error
	switch {
	case r.session == "auto" && r.settings.Trebi:
		r.rm, err = r.trebiSession()
		return r.rm, err
	case r.settings.Missing() != nil && r.settings.Trebi:
		return nil, usage(r.settings.Missing())
	case r.settings.Missing() != nil:
		return nil, usage(fmt.Errorf("no server: use --server, --target, or %s", config.EnvServer))
	case r.session == "off", r.opts.Cursor != "" && r.opts.Cursor != vnc.CursorNone:
		// TODO(TREBI-496): the holder draws the cursor after the decoder merge.
		r.rm, err = r.withPassword(func(pw string) (remote, error) {
			o := r.opts
			o.Password = pw
			c, err := vnc.Dial(ctx, o)
			if err != nil {
				return nil, session.Explain(err)
			}
			return session.NewLocal(c, false), nil
		})
	default:
		p, perr := r.sessionPaths()
		if perr != nil {
			return nil, perr
		}
		r.rm, err = r.withPassword(func(pw string) (remote, error) {
			return session.Start(ctx, p, r.holderArgs(), holderEnv(pw))
		})
	}
	if err != nil {
		r.rm = nil
		return nil, err
	}
	return r.rm, nil
}

// withPassword connects. When the server asks for a password that is not
// set, it asks on the terminal and connects again.
func (r *Runner) withPassword(connect func(pw string) (remote, error)) (remote, error) {
	pw := r.settings.Password
	rm, err := connect(pw)
	if errors.Is(err, vnc.ErrAuth) && pw == "" && r.prompt != nil && !r.settings.Trebi {
		if pw, err = r.prompt("Password for " + r.opts.Addr); err != nil {
			return nil, err
		}
		rm, err = connect(pw)
	}
	return rm, err
}

func (r *Runner) close() error {
	if r.rm == nil {
		return nil
	}
	err := r.rm.Close()
	r.rm = nil
	return err
}

// do runs the steps on one connection, with the delay between them.
func (r *Runner) do(ctx context.Context, steps ...step) error {
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}
	rm, err := r.remote(ctx)
	if err != nil {
		return err
	}
	for i, s := range steps {
		if i > 0 && !r.delay.zero() {
			if err := sleep(ctx, r.delay.pick()); err != nil {
				return err
			}
		}
		if err := s(ctx, rm); err != nil {
			return err
		}
	}
	return nil
}

// print writes v as JSON with --json, else the text.
func (r *Runner) print(v any, text string) error {
	if r.json {
		return json.NewEncoder(r.stdout).Encode(v)
	}
	if text != "" {
		_, err := fmt.Fprintln(r.stdout, text)
		return err
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// delayRange is a random time from min to max.
type delayRange struct{ min, max time.Duration }

func parseDelay(s string) (delayRange, error) {
	if s == "" {
		return delayRange{}, nil
	}
	lo, hi, rng := strings.Cut(s, "-")
	a, err1 := strconv.Atoi(strings.TrimSpace(lo))
	b := a
	var err2 error
	if rng {
		b, err2 = strconv.Atoi(strings.TrimSpace(hi))
	}
	if err1 != nil || err2 != nil || a < 0 || b < a {
		return delayRange{}, fmt.Errorf("--delay: %q is not N or MIN-MAX milliseconds", s)
	}
	return delayRange{time.Duration(a) * time.Millisecond, time.Duration(b) * time.Millisecond}, nil
}

func (d delayRange) zero() bool { return d.max == 0 }

func (d delayRange) pick() time.Duration {
	if d.max <= d.min {
		return d.min
	}
	return d.min + rand.N(d.max-d.min+1)
}
