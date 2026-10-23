package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/session"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
)

// errTrebiSession is a session command that Trebi does for the connection.
var errTrebiSession = errors.New("Trebi keeps the session of this connection") //nolint:staticcheck // Trebi is a name

// connectWait is the longest time for one connect of a holder.
const connectWait = 30 * time.Second

func (r *Runner) sessionCommand() *cli.Command {
	return &cli.Command{
		Name:  "session",
		Usage: "Keep one connection open in the background, so that commands start at once",
		Commands: []*cli.Command{
			{
				Name:  "start",
				Usage: "Start the session of the server, or show the one that runs",
				Action: func(ctx context.Context, c *cli.Command) error {
					if r.settings.Trebi {
						return usage(errTrebiSession)
					}
					r.session = "auto"
					rm, err := r.remote(ctx)
					if err != nil {
						return err
					}
					st, err := rm.(*session.Client).Status(ctx)
					if err != nil {
						return err
					}
					return r.printStatus(st)
				},
			},
			{
				Name:  "status",
				Usage: "Show the session of the server",
				Action: func(ctx context.Context, c *cli.Command) error {
					cl, err := r.openSession()
					if err != nil {
						return err
					}
					if cl == nil {
						return r.print(map[string]bool{"running": false}, "No session runs for "+r.settings.Server+".")
					}
					defer cl.Close()
					st, err := cl.Status(ctx)
					if err != nil {
						return err
					}
					return r.printStatus(st)
				},
			},
			{
				Name:  "stop",
				Usage: "Stop the session of the server",
				Flags: []cli.Flag{&cli.BoolFlag{Name: "all", Usage: "stop all sessions"}},
				Action: func(ctx context.Context, c *cli.Command) error {
					if r.settings.Trebi {
						return usage(errTrebiSession)
					}
					if c.Bool("all") {
						return r.eachSession(ctx, func(cl *session.Client, _ *session.Status) error { return cl.Stop(ctx) })
					}
					cl, err := r.openSession()
					if err != nil || cl == nil {
						return err
					}
					defer cl.Close()
					return cl.Stop(ctx)
				},
			},
			{
				Name:  "list",
				Usage: "List the sessions that run",
				Action: func(ctx context.Context, c *cli.Command) error {
					if r.settings.Trebi {
						return usage(errTrebiSession)
					}
					var all []*session.Status
					err := r.eachSession(ctx, func(_ *session.Client, st *session.Status) error {
						all = append(all, st)
						return nil
					})
					if err != nil {
						return err
					}
					if r.json {
						return r.print(all, "")
					}
					for _, st := range all {
						fmt.Fprintln(r.stdout, statusText(st))
					}
					return nil
				},
			},
			{
				Name:   "run",
				Usage:  "Run the session in this process (vnc-cli starts it)",
				Hidden: true,
				Flags:  []cli.Flag{&cli.IntFlag{Name: "status-fd", Usage: "report the start on this descriptor"}},
				Action: func(ctx context.Context, c *cli.Command) error {
					var report *os.File
					if fd := c.Int("status-fd"); fd > 0 {
						report = os.NewFile(uintptr(fd), "status")
					}
					err := r.runHolder(ctx, report)
					if err != nil {
						fmt.Fprintf(r.stderr, "error: %v\n", err)
					}
					return err
				},
			},
		},
	}
}

// runHolder is the background session. It reports the first connect on
// report, then serves the socket until the idle timeout.
func (r *Runner) runHolder(ctx context.Context, report *os.File) (err error) {
	defer func() { session.Report(report, err) }()
	if r.settings.Trebi {
		return usage(errTrebiSession)
	}
	if m := r.settings.Missing(); m != nil {
		return usage(m)
	}
	p, err := r.sessionPaths()
	if err != nil {
		return err
	}
	lock, err := session.Lock(p.Lock)
	if errors.Is(err, session.ErrBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Close()

	level := slog.LevelInfo
	if r.verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(r.stderr, &slog.HandlerOptions{Level: level}))
	h := session.NewHolder(r.dialer(r.settings.Password), r.settings.Server, r.settings.User, r.settings.IdleTimeout, log)
	cctx, cancel := context.WithTimeout(ctx, connectWait)
	err = h.Connect(cctx)
	cancel()
	if err != nil {
		return err
	}
	ln, err := session.Listen(p.Sock)
	if err != nil {
		return err
	}
	session.Report(report, nil)
	report = nil
	log.Info("the session started", "server", r.settings.Server, "idle_timeout", r.settings.IdleTimeout)
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	err = h.Serve(ctx, ln)
	log.Info("the session stopped", "err", err)
	return err
}

// dialer connects with the options of the runner and the password.
func (r *Runner) dialer(password string) func(context.Context) (*vnc.Conn, error) {
	o := r.opts
	o.Password = password
	return func(ctx context.Context) (*vnc.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, connectWait)
		defer cancel()
		c, err := vnc.Dial(ctx, o)
		return c, session.Explain(err)
	}
}

// sessionKey names the holder: the server, the user, and the flags that
// shape the connection.
func (r *Runner) sessionKey() string {
	return strings.Join(append([]string{r.settings.Key()}, r.connArgs...), " ")
}

func (r *Runner) sessionPaths() (session.Paths, error) {
	p, err := session.StandalonePaths(r.settings.Dir, r.sessionKey())
	if err != nil {
		return p, usage(err)
	}
	return p, nil
}

// holderArgs are the arguments of a new holder. The password is not in
// them: it goes in the env.
func (r *Runner) holderArgs() []string {
	args := []string{"--server", r.settings.Server}
	if r.settings.User != "" {
		args = append(args, "--user", r.settings.User)
	}
	args = append(args, r.connArgs...)
	args = append(args, "--idle-timeout", r.settings.IdleTimeout.String())
	if r.verbose {
		args = append(args, "--verbose")
	}
	return append(args, "session", "run", "--status-fd", fmt.Sprint(session.StatusFD))
}

// holderEnv is the env of this process with the password of the holder in
// VNC_PASSWORD and no other VNC_* value.
func holderEnv(password string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "VNC_") {
			env = append(env, kv)
		}
	}
	if password != "" {
		env = append(env, config.EnvPassword+"="+password)
	}
	return env
}

// trebiSession opens the socket of serve.
func (r *Runner) trebiSession() (remote, error) {
	p, err := session.TrebiPaths(r.settings.StateDir)
	if err != nil {
		return nil, usage(err)
	}
	cl, err := session.Open(p.Sock)
	if err != nil {
		return nil, &session.RemoteError{Kind: session.KindConnect, Message: "the connection to this computer does not run in Trebi: start it in Trebi, or use --session off"}
	}
	return cl, nil
}

// openSession opens the holder of the server, or returns nil when none
// runs.
func (r *Runner) openSession() (*session.Client, error) {
	if r.settings.Trebi {
		rm, err := r.trebiSession()
		if err != nil {
			return nil, nil
		}
		return rm.(*session.Client), nil
	}
	if m := r.settings.Missing(); m != nil {
		return nil, usage(fmt.Errorf("no server: use --server, --target, or %s", config.EnvServer))
	}
	p, err := r.sessionPaths()
	if err != nil {
		return nil, err
	}
	cl, err := session.Open(p.Sock)
	if err != nil {
		return nil, nil
	}
	return cl, nil
}

// eachSession calls fn for each holder in the run folder.
func (r *Runner) eachSession(ctx context.Context, fn func(*session.Client, *session.Status) error) error {
	socks, _ := filepath.Glob(filepath.Join(r.settings.Dir, "run", "*.sock"))
	for _, s := range socks {
		cl, err := session.Open(s)
		if err != nil {
			continue
		}
		st, err := cl.Status(ctx)
		if err == nil {
			err = fn(cl, st)
		}
		cl.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) printStatus(st *session.Status) error { return r.print(st, statusText(st)) }

func statusText(st *session.Status) string {
	var b strings.Builder
	who := st.Server
	if st.User != "" {
		who = st.User + "@" + st.Server
	}
	fmt.Fprintf(&b, "%s", who)
	if st.Connected {
		fmt.Fprintf(&b, "  %q %dx%d", st.Name, st.Width, st.Height)
	} else {
		b.WriteString("  connecting")
	}
	fmt.Fprintf(&b, "  pid %d  up %s  clients %d", st.PID, time.Duration(st.UptimeSec)*time.Second, st.Clients)
	if st.IdleLeftSec != nil {
		fmt.Fprintf(&b, "  stops in %s", time.Duration(*st.IdleLeftSec)*time.Second)
	}
	return b.String()
}
