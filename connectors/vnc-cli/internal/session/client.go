package session

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Client runs the commands in a holder through its socket. It has the same
// methods as Local.
type Client struct {
	nc net.Conn
	r  *bufio.Reader
	w  *bufio.Writer
}

// Open connects to the socket of a running holder.
func Open(path string) (*Client, error) {
	nc, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, err
	}
	return &Client{nc: nc, r: bufio.NewReaderSize(nc, 64<<10), w: bufio.NewWriterSize(nc, 64<<10)}, nil
}

// Close closes the socket.
func (c *Client) Close() error { return c.nc.Close() }

func (c *Client) call(ctx context.Context, req Request, pix []byte) (Reply, []byte, error) {
	if dl, ok := ctx.Deadline(); ok {
		if req.Timeout = time.Until(dl); req.Timeout <= 0 {
			return Reply{}, nil, context.DeadlineExceeded
		}
	}
	stop := context.AfterFunc(ctx, func() { c.nc.SetDeadline(time.Now()) })
	var rep Reply
	err := writeMsg(c.w, req, pix)
	var out []byte
	if err == nil {
		out, err = readMsg(c.r, &rep, func() *Frame { return rep.Image })
	}
	if !stop() {
		return Reply{}, nil, ctx.Err()
	}
	if err != nil {
		return Reply{}, nil, &RemoteError{Kind: KindClosed, Message: fmt.Sprintf("the session closed: %v", err)}
	}
	if rep.Error != nil {
		return rep, nil, rep.Error
	}
	return rep, out, nil
}

// Info describes the screen.
func (c *Client) Info(ctx context.Context) (Info, error) {
	rep, _, err := c.call(ctx, Request{Op: OpInfo}, nil)
	if err != nil || rep.Info == nil {
		return Info{}, err
	}
	return *rep.Info, nil
}

// Capture copies the area r of the screen.
func (c *Client) Capture(ctx context.Context, r image.Rectangle) (*image.RGBA, error) {
	rep, pix, err := c.call(ctx, Request{Op: OpCapture, Rect: r}, nil)
	if err != nil {
		return nil, err
	}
	if rep.Image == nil {
		return nil, errors.New("the session sent no image")
	}
	return rep.Image.image(pix), nil
}

// Input sends the actions in order.
func (c *Client) Input(ctx context.Context, actions []Action) error {
	_, _, err := c.call(ctx, Request{Op: OpInput, Actions: actions}, nil)
	return err
}

// Paste sets the clipboard of the remote computer.
func (c *Client) Paste(ctx context.Context, text string) error {
	_, _, err := c.call(ctx, Request{Op: OpPaste, Text: text}, nil)
	return err
}

// Clipboard is the last clipboard text of the remote computer.
func (c *Client) Clipboard(ctx context.Context) (string, error) {
	rep, _, err := c.call(ctx, Request{Op: OpClipboard}, nil)
	return rep.Text, err
}

// Expect waits until the area at the point matches ref.
func (c *Client) Expect(ctx context.Context, ref image.Image, at image.Point, m Match) error {
	rgba, ok := ref.(*image.RGBA)
	if !ok || rgba.Rect.Min != (image.Point{}) {
		rgba = image.NewRGBA(image.Rectangle{Max: ref.Bounds().Size()})
		draw.Draw(rgba, rgba.Rect, ref, ref.Bounds().Min, draw.Src)
	}
	f, pix := frameOf(rgba)
	_, _, err := c.call(ctx, Request{Op: OpExpect, At: at, Match: m, Image: f}, pix)
	return err
}

// Stable waits until the area shows no change for d.
func (c *Client) Stable(ctx context.Context, d time.Duration, r image.Rectangle, m Match) error {
	_, _, err := c.call(ctx, Request{Op: OpStable, For: d, Rect: r, Match: m}, nil)
	return err
}

// Status describes the holder.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	rep, _, err := c.call(ctx, Request{Op: OpStatus}, nil)
	return rep.Status, err
}

// Stop stops the holder.
func (c *Client) Stop(ctx context.Context) error {
	_, _, err := c.call(ctx, Request{Op: OpStop}, nil)
	return err
}

// startWait is the longest time to wait for a new holder to connect.
const startWait = 30 * time.Second

// StatusFD is the descriptor on which a new holder reports its start.
const StatusFD = 3

// startReport is the line that a new holder writes on StatusFD.
type startReport struct {
	Error *RemoteError `json:"error,omitempty"`
}

// Start opens the holder at p, or starts it when none runs: the program
// runs again with args and env, in its own session, with its log in
// p.Log. The password goes in env, never in args.
func Start(ctx context.Context, p Paths, args, env []string) (*Client, error) {
	if c, err := Open(p.Sock); err == nil {
		return c, nil
	}
	if err := os.MkdirAll(filepath.Dir(p.Sock), 0o700); err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	logf, err := openLog(p.Log)
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer pr.Close()
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.ExtraFiles = []*os.File{pw}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	pw.Close()
	if err != nil {
		return nil, fmt.Errorf("start the session: %w", err)
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()

	dl := time.Now().Add(startWait)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	pr.SetReadDeadline(dl)
	var rep startReport
	line, rerr := bufio.NewReader(pr).ReadBytes('\n')
	switch {
	case rerr == nil:
		if err := json.Unmarshal(line, &rep); err != nil {
			return nil, fmt.Errorf("the session sent a bad start report: %w", err)
		}
	case errors.Is(rerr, os.ErrDeadlineExceeded):
		cmd.Process.Kill()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &RemoteError{Kind: KindConnect, Message: fmt.Sprintf("the session did not connect in %s (log: %s)", startWait, p.Log)}
	default:
		<-exited
		return nil, fmt.Errorf("the session stopped: %s (log: %s)", lastLine(p.Log), p.Log)
	}
	if rep.Error != nil {
		<-exited
		return nil, rep.Error
	}
	// A holder that lost the lock race reports at once; the winner may
	// still connect.
	for {
		c, err := Open(p.Sock)
		if err == nil || time.Now().After(dl) {
			return c, err
		}
		if err := sleep(ctx, 50*time.Millisecond); err != nil {
			return nil, err
		}
	}
}

// Report writes the start report of a holder on f and closes f.
func Report(f *os.File, err error) {
	if f == nil {
		return
	}
	var rep startReport
	if err != nil {
		rep.Error = remoteError(err)
	}
	b, _ := json.Marshal(rep)
	f.Write(append(b, '\n'))
	f.Close()
}

// Explain adds a hint to a certificate error.
func Explain(err error) error {
	var (
		untrusted *tls.CertificateVerificationError
		badName   x509.HostnameError
	)
	if errors.As(err, &untrusted) || errors.As(err, &badName) {
		return fmt.Errorf("%w (use --tls-ca-cert FILE or --tls-insecure-skip-verify)", err)
	}
	return err
}
