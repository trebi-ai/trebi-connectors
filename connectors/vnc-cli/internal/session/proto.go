package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/watch"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
)

// Socket operations. A request and a reply are one JSON line each. An
// image follows its line as raw RGBA bytes, so no picture goes through JSON.
const (
	OpInfo      = "info"
	OpCapture   = "capture"
	OpInput     = "input"
	OpPaste     = "paste"
	OpClipboard = "clipboard"
	OpExpect    = "expect"
	OpStable    = "stable"
	OpStatus    = "status"
	OpStop      = "stop"
	// OpWatch is a stream: each event is a reply, until the client
	// closes the socket or an error reply ends it.
	OpWatch = "watch"
)

// Request is one command to a holder.
type Request struct {
	Op      string          `json:"op"`
	Rect    image.Rectangle `json:"rect,omitzero"`
	At      image.Point     `json:"at,omitzero"`
	Actions []Action        `json:"actions,omitempty"`
	Text    string          `json:"text,omitempty"`
	Match   Match           `json:"match,omitzero"`
	For     time.Duration   `json:"for,omitempty"`
	// Timeout is the time that the client has left. Zero is no limit.
	Timeout time.Duration `json:"timeout,omitempty"`
	Image   *Frame        `json:"image,omitempty"`
	Watch   *watch.Spec   `json:"watch,omitempty"`
}

// Reply is the answer of a holder.
type Reply struct {
	Error  *RemoteError `json:"error,omitempty"`
	Info   *Info        `json:"info,omitempty"`
	Text   string       `json:"text,omitempty"`
	Image  *Frame       `json:"image,omitempty"`
	Status *Status      `json:"status,omitempty"`
	Event  *watch.Event `json:"event,omitempty"`
}

// Frame is the size of the RGBA bytes after the line.
type Frame struct {
	W int `json:"w"`
	H int `json:"h"`
}

// maxSide limits the image that a peer can make the other side allocate.
const maxSide = 1 << 14

func (f *Frame) size() (int, error) {
	if f.W < 0 || f.H < 0 || f.W > maxSide || f.H > maxSide {
		return 0, fmt.Errorf("image size %dx%d is not valid", f.W, f.H)
	}
	return f.W * f.H * 4, nil
}

func frameOf(img *image.RGBA) (*Frame, []byte) {
	b := img.Bounds()
	f := &Frame{W: b.Dx(), H: b.Dy()}
	if img.Stride == 4*f.W {
		return f, img.Pix[:4*f.W*f.H]
	}
	pix := make([]byte, 0, 4*f.W*f.H)
	for y := 0; y < f.H; y++ {
		pix = append(pix, img.Pix[y*img.Stride:y*img.Stride+4*f.W]...)
	}
	return f, pix
}

func (f *Frame) image(pix []byte) *image.RGBA {
	return &image.RGBA{Pix: pix, Stride: 4 * f.W, Rect: image.Rect(0, 0, f.W, f.H)}
}

// Status describes a holder.
type Status struct {
	Server      string `json:"server"`
	User        string `json:"user,omitempty"`
	Name        string `json:"name,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	Connected   bool   `json:"connected"`
	PID         int    `json:"pid"`
	UptimeSec   int    `json:"uptime_sec"`
	Clients     int    `json:"clients"`
	IdleLeftSec *int   `json:"idle_left_sec,omitempty"`
}

// Error kinds on the socket. They keep the exit code of an error from the
// holder.
const (
	KindAuth     = "auth"
	KindConnect  = "connect"
	KindClosed   = "closed"
	KindProtocol = "protocol"
	KindFailed   = "failed"
	KindTimeout  = "timeout"
	KindOther    = "other"
)

// RemoteError is an error from a holder.
type RemoteError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

func (e *RemoteError) Error() string { return e.Message }

// Is matches the sentinel of the kind.
func (e *RemoteError) Is(target error) bool {
	switch e.Kind {
	case KindAuth:
		return target == vnc.ErrAuth
	case KindClosed:
		return target == vnc.ErrClosed
	case KindTimeout:
		return target == context.DeadlineExceeded
	}
	return false
}

func remoteError(err error) *RemoteError {
	var (
		de *vnc.DialError
		pe *vnc.ProtocolError
		re *RemoteError
	)
	kind := KindOther
	switch {
	case errors.As(err, &re):
		return re
	case errors.Is(err, context.DeadlineExceeded):
		kind = KindTimeout
	case errors.Is(err, vnc.ErrAuth):
		kind = KindAuth
	case errors.As(err, &de):
		kind = KindConnect
	case errors.Is(err, vnc.ErrClosed):
		kind = KindClosed
	case errors.As(err, &pe):
		kind = KindProtocol
	case errors.Is(err, ErrOffScreen), errors.Is(err, watch.ErrArea):
		kind = KindFailed
	}
	return &RemoteError{Kind: kind, Message: err.Error()}
}

// writeMsg writes v and the bytes of its image.
func writeMsg(w *bufio.Writer, v any, pix []byte) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.Write(append(b, '\n'))
	w.Write(pix)
	return w.Flush()
}

// readMsg reads one line into v, then the bytes of the image that the line
// names.
func readMsg(r *bufio.Reader, v any, img func() *Frame) ([]byte, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(line, v); err != nil {
		return nil, fmt.Errorf("bad message: %w", err)
	}
	f := img()
	if f == nil {
		return nil, nil
	}
	n, err := f.size()
	if err != nil {
		return nil, err
	}
	pix := make([]byte, n)
	_, err = io.ReadFull(r, pix)
	return pix, err
}
