package sdk

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Env names of the folder contract.
const (
	EnvStateDir = "TREBI_STATE_DIR"
	EnvCacheDir = "TREBI_CACHE_DIR"
)

// Trebi is the folder contract that the daemon gives to a program.
type Trebi struct {
	StateDir string // TREBI_STATE_DIR: durable data
	CacheDir string // TREBI_CACHE_DIR: data that the program can build again
}

// FromEnv reads the contract. ok is false outside Trebi.
func FromEnv() (t Trebi, ok bool) {
	t = Trebi{StateDir: os.Getenv(EnvStateDir), CacheDir: os.Getenv(EnvCacheDir)}
	return t, t.StateDir != ""
}

// WriteFileAtomic writes b to path through the temp file path+".tmp", a
// sync, and a rename. A crash leaves the old file or the new file, never
// part of a file. Do not write one path from two goroutines at a time.
func WriteFileAtomic(path string, b []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync() // not all file systems sync a folder
		_ = d.Close()
	}
	return nil
}

// FolderUser gets the folders of the session before initialize. Serve
// calls UseFolders with the values of WithStateDir and WithCacheDir, or of
// the env.
type FolderUser interface {
	UseFolders(t Trebi) error
}

// MissingInput is the error a program returns when a required input is
// not set in Trebi mode. Serve reports it as status auth_required with
// reason missing_input and the input name in the message.
type MissingInput struct{ Name, Label string }

func (e MissingInput) Error() string {
	if e.Label == "" {
		return e.Name + " is missing"
	}
	return e.Label + " (" + e.Name + ") is missing"
}

// asMissingInput finds a MissingInput value or pointer in err.
func asMissingInput(err error) (MissingInput, bool) {
	var v MissingInput
	if errors.As(err, &v) {
		return v, true
	}
	var p *MissingInput
	if errors.As(err, &p) && p != nil {
		return *p, true
	}
	return MissingInput{}, false
}

// LogTimeLayout is the time of a log line outside Trebi: local time, as
// 2006-01-02 15:04:05.
const LogTimeLayout = time.DateTime

// NewLogHandler returns a text handler on w that has no time attribute. In
// Trebi mode the daemon stamps each line, so the line has no time. Outside
// Trebi, each line starts with the local time of the record in
// LogTimeLayout and one space. It keeps Level, AddSource, and ReplaceAttr
// of opts. ReplaceAttr never sees the time key.
func NewLogHandler(w io.Writer, trebi bool, opts *slog.HandlerOptions) slog.Handler {
	var o slog.HandlerOptions
	if opts != nil {
		o = *opts
	}
	user := o.ReplaceAttr
	o.ReplaceAttr = func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		if user != nil {
			return user(groups, a)
		}
		return a
	}
	if trebi {
		return slog.NewTextHandler(w, &o)
	}
	sink := &logSink{w: w}
	return &stampHandler{inner: slog.NewTextHandler(sink, &o), sink: sink}
}

// SetDefaultLogger sets slog.Default to NewLogHandler on stderr. It reads
// the mode from FromEnv. Call it first in main, before a constructor reads
// slog.Default.
func SetDefaultLogger(opts *slog.HandlerOptions) *slog.Logger {
	_, trebi := FromEnv()
	l := slog.New(NewLogHandler(os.Stderr, trebi, opts))
	slog.SetDefault(l)
	return l
}

// logSink collects the text of one record, so that the time and the record
// go out in one Write. The mutex is shared by all derived handlers.
type logSink struct {
	mu  sync.Mutex
	w   io.Writer
	buf bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) { return s.buf.Write(p) }

type stampHandler struct {
	inner slog.Handler
	sink  *logSink
}

func (h *stampHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *stampHandler) Handle(ctx context.Context, r slog.Record) error {
	t := r.Time
	if t.IsZero() {
		t = time.Now()
	}
	s := h.sink
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.Reset()
	s.buf.WriteString(t.Local().Format(LogTimeLayout))
	s.buf.WriteByte(' ')
	if err := h.inner.Handle(ctx, r); err != nil {
		return err
	}
	_, err := s.w.Write(s.buf.Bytes())
	return err
}

func (h *stampHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &stampHandler{inner: h.inner.WithAttrs(as), sink: h.sink}
}

func (h *stampHandler) WithGroup(name string) slog.Handler {
	return &stampHandler{inner: h.inner.WithGroup(name), sink: h.sink}
}
