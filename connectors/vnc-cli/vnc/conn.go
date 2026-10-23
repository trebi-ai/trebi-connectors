// Package vnc is an RFB (VNC) client. It is a Go port of the protocol parts
// of vncdotool (MIT, see NOTICE).
package vnc

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"
)

// Options configure a connection.
type Options struct {
	Addr     string // see ParseAddr
	Password string
	User     string // ARD and VeNCrypt Plain

	// Exclusive asks the server to disconnect the other viewers. The
	// default is a shared session.
	Exclusive bool

	// Encodings replaces the default encoding list. Pseudo-encodings for
	// the features below are added to it.
	Encodings []int32
	// JPEG allows lossy Tight JPEG rectangles at JPEGQuality (0-9).
	JPEG        bool
	JPEGQuality int
	// CompressLevel asks for a zlib level (1-9). Zero keeps the default of
	// the server.
	CompressLevel int
	// Cursor selects how the client gets the pointer shape.
	Cursor CursorMode
	// PixelFormat forces a pixel format. Nil keeps the native format of the
	// server when the client can read it, else asks for bgrx8888.
	PixelFormat *PixelFormat

	// Security is the order of security types to try. Nil prefers
	// VeNCrypt (TLS); when it has no usable subtype, Dial connects again
	// without it.
	Security []uint8
	// TLS is the VeNCrypt and wss setting (see NewTLSConfig). An empty
	// ServerName gets the host of Addr.
	TLS *tls.Config

	// UpdateInterval is the shortest time between two automatic incremental
	// update requests. Zero means 50ms.
	UpdateInterval time.Duration

	Logger *slog.Logger
}

// CursorMode is how the pointer shape reaches the client.
type CursorMode string

const (
	// CursorNone leaves the pointer out of captures (the default).
	CursorNone CursorMode = "none"
	// CursorLocal draws the pointer shape from the server into captures.
	CursorLocal CursorMode = "local"
	// CursorServer lets the server draw the pointer into the framebuffer.
	CursorServer CursorMode = "server"
)

func (o *Options) securityOrder() []uint8 {
	if o.Security != nil {
		return o.Security
	}
	order := []uint8{SecVeNCrypt}
	if o.User != "" {
		order = append(order, SecAppleARD)
	}
	if o.Password != "" {
		order = append(order, SecVNC)
	}
	return append(order, SecNone, SecVNC, SecAppleARD)
}

func (o *Options) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// Conn is one RFB connection. One goroutine reads and decodes; the methods
// are safe for concurrent use.
type Conn struct {
	opts Options
	log  *slog.Logger
	nc   net.Conn
	r    *reader

	wmu sync.Mutex

	minor int
	name  string
	fb    *framebuffer

	// Owned by the read goroutine.
	pf       PixelFormat
	cv       *converter
	colors   [256][3]uint8
	decoders map[int32]decoder

	mu       sync.Mutex
	seq      uint64
	subs     map[*Subscription]struct{}
	waiters  map[*waiter]struct{}
	first    *waiter // the full update that NewConn asks for
	clip     string
	lastReq  time.Time
	pending  bool // an incremental request is out
	features map[string]bool
	encs     []int32
	cursor   *Cursor
	pointer  image.Point
	movedAt  time.Time // of the last Pointer call

	screens    []Screen
	resizeWait chan uint16 // the answer to SetDesktopSize
	fences     map[uint32]chan struct{}
	fenceID    uint32
	continuous bool
	clipFlags  uint32 // the extended clipboard capabilities of the server
	clipLimit  uint32
	clipOut    string // the last text from SetClipboard
	clipOwner  bool

	resizeMu sync.Mutex
	replies  chan []byte

	done      chan struct{}
	err       error
	closeOnce sync.Once
}

// Dial connects, logs in, and starts the read loop. The first full update
// is requested; use Refresh to wait for it.
func Dial(ctx context.Context, o Options) (*Conn, error) {
	a, err := ParseAddr(o.Addr)
	if err != nil {
		return nil, err
	}
	c, err := dialOnce(ctx, a, o)
	if errors.Is(err, errVeNCryptUnusable) && o.Security == nil {
		// Xvnc offers TLSVnc and VncAuth by default, and Go has no anonymous
		// TLS. Connect again without VeNCrypt.
		o.Security = slices.DeleteFunc(o.securityOrder(), func(t uint8) bool { return t == SecVeNCrypt })
		c2, err2 := dialOnce(ctx, a, o)
		if !errors.Is(err2, errNoCommonType) {
			return c2, err2
		}
	}
	return c, err
}

func dialOnce(ctx context.Context, a Addr, o Options) (*Conn, error) {
	nc, err := dialAddr(ctx, a, &o)
	if err != nil {
		return nil, &DialError{Addr: a.String(), Err: err}
	}
	c, err := NewConn(ctx, nc, o)
	if err != nil {
		nc.Close()
		return nil, err
	}
	return c, nil
}

func dialAddr(ctx context.Context, a Addr, o *Options) (net.Conn, error) {
	switch a.Network {
	case "tcp", "unix":
		var d net.Dialer
		return d.DialContext(ctx, a.Network, a.Address)
	case "ws", "wss":
		return dialWebSocket(ctx, a.Address, o)
	default:
		return nil, fmt.Errorf("%s addresses are not supported", a.Network)
	}
}

// NewConn runs the handshake over nc. Tests use it over net.Pipe.
func NewConn(ctx context.Context, nc net.Conn, o Options) (*Conn, error) {
	c := &Conn{
		opts:     o,
		log:      o.logger(),
		nc:       nc,
		subs:     map[*Subscription]struct{}{},
		waiters:  map[*waiter]struct{}{},
		features: map[string]bool{},
		fences:   map[uint32]chan struct{}{},
		replies:  make(chan []byte, 64),
		done:     make(chan struct{}),
	}
	stop := context.AfterFunc(ctx, func() { nc.SetDeadline(time.Unix(1, 0)) })
	err := c.handshake()
	if !stop() {
		if err == nil {
			err = ctx.Err()
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			a, _ := ParseAddr(o.Addr)
			err = fmt.Errorf("log in to %s: %w", a, ctx.Err())
		}
		return nil, err
	}
	nc.SetDeadline(time.Time{})
	c.first = &waiter{cov: newCoverage(c.fb.bounds()), ch: make(chan struct{})}
	c.waiters[c.first] = struct{}{}
	go c.loop()
	go c.replyLoop()
	if err := c.requestUpdate(false); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Conn) handshake() error {
	br := bufio.NewReaderSize(c.nc, 64<<10)
	a, _ := ParseAddr(c.opts.Addr)
	h := &handshake{conn: c.nc, r: br, opts: &c.opts, host: a.Host()}

	ver, err := h.read(12)
	if err != nil {
		return hsErr("version", err)
	}
	major, minor, ok := parseVersion(ver)
	if !ok {
		return protoErr("version", "not an RFB server: %q", ver)
	}
	switch {
	case major == 3 && minor >= 8, major > 3:
		h.minor = 8
	case major == 3 && minor == 7:
		h.minor = 7
	default:
		h.minor = 3
	}
	c.log.Debug("rfb version", "server", fmt.Sprintf("%d.%d", major, minor), "client", h.minor)
	if err := h.write(fmt.Appendf(nil, "RFB 003.%03d\n", h.minor)); err != nil {
		return hsErr("version", err)
	}

	var sec uint8
	if h.minor == 3 {
		t, err := h.u32()
		if err != nil {
			return hsErr("security", err)
		}
		if t == 0 {
			reason, _ := h.reason()
			return protoErr("security", "the server refused the connection: %s", reason)
		}
		if t > 255 {
			return protoErr("security", "unknown security type %d", t)
		}
		sec = uint8(t)
		if _, ok := securityTypes[sec]; !ok {
			return protoErr("security", "the server requires %s, which is not supported", securityName(sec))
		}
	} else {
		n, err := h.read(1)
		if err != nil {
			return hsErr("security", err)
		}
		if n[0] == 0 {
			reason, _ := h.reason()
			return protoErr("security", "the server refused the connection: %s", reason)
		}
		offered, err := h.read(int(n[0]))
		if err != nil {
			return hsErr("security", err)
		}
		if sec, err = chooseSecurity(offered, &c.opts); err != nil {
			return err
		}
		if err := h.write([]byte{sec}); err != nil {
			return hsErr("security", err)
		}
	}
	c.log.Debug("rfb security", "type", securityName(sec))
	if err := securityTypes[sec]().handshake(h); err != nil {
		var ae *AuthError
		var pe *ProtocolError
		if errors.As(err, &ae) || errors.As(err, &pe) {
			return err
		}
		return hsErr(securityName(sec), err)
	}
	c.nc, c.minor = h.conn, h.minor

	shared := byte(1)
	if c.opts.Exclusive {
		shared = 0
	}
	if err := h.write([]byte{shared}); err != nil {
		return hsErr("client init", err)
	}
	si, err := h.read(24)
	if err != nil {
		return hsErr("server init", err)
	}
	w, ht := int(binary.BigEndian.Uint16(si)), int(binary.BigEndian.Uint16(si[2:]))
	native := ParsePixelFormat(si[4:20])
	nameLen := binary.BigEndian.Uint32(si[20:])
	if nameLen > maxReasonLen {
		return protoErr("server init", "name of %d bytes is too long", nameLen)
	}
	name, err := h.read(int(nameLen))
	if err != nil {
		return hsErr("server init", err)
	}
	c.name = string(name)
	c.fb = newFramebuffer(w, ht)
	c.r = newReader(h.r)
	c.log.Debug("rfb server init", "size", fmt.Sprintf("%dx%d", w, ht), "format", native.String(), "name", c.name)

	pf := native
	switch {
	case c.opts.PixelFormat != nil:
		pf = *c.opts.PixelFormat
	case !native.TrueColor || native.Validate() != nil:
		pf = FormatBGRX8888
	}
	if pf != native {
		if err := c.write(append([]byte{msgSetPixelFormat, 0, 0, 0}, pf.Bytes()...)); err != nil {
			return hsErr("set pixel format", err)
		}
	}
	if err := c.setFormat(pf); err != nil {
		return err
	}
	c.decoders = newDecoders()
	return c.setEncodings()
}

func hsErr(step string, err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		err = fmt.Errorf("the server closed the connection")
	}
	return &ProtocolError{Step: step, Err: err}
}

func parseVersion(b []byte) (major, minor int, ok bool) {
	if len(b) != 12 || string(b[:4]) != "RFB " || b[7] != '.' || b[11] != '\n' {
		return 0, 0, false
	}
	major, e1 := strconv.Atoi(string(b[4:7]))
	minor, e2 := strconv.Atoi(string(b[8:11]))
	return major, minor, e1 == nil && e2 == nil
}

func (c *Conn) setFormat(pf PixelFormat) error {
	cv, err := newConverter(pf, &c.colors)
	if err != nil {
		return protoErr("pixel format", "%v", err)
	}
	c.mu.Lock()
	c.pf, c.cv = pf, cv
	c.mu.Unlock()
	return nil
}

// Client to server message types.
const (
	msgSetPixelFormat     = 0
	msgSetEncodings       = 2
	msgFramebufferRequest = 3
	msgKeyEvent           = 4
	msgPointerEvent       = 5
	msgClientCutText      = 6
)

// Server to client message types.
const (
	msgFramebufferUpdate = 0
	msgSetColourMap      = 1
	msgBell              = 2
	msgServerCutText     = 3
)

func (c *Conn) write(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.nc.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, err := c.nc.Write(b)
	if err != nil {
		select {
		case <-c.done:
			return c.err
		default:
		}
	}
	return err
}

// reply queues a message from the read goroutine. A write there could wait
// for the server, while the server waits for the client to read.
func (c *Conn) reply(b []byte) {
	select {
	case c.replies <- b:
	case <-c.done:
	}
}

func (c *Conn) replyLoop() {
	for {
		select {
		case b := <-c.replies:
			if err := c.write(b); err != nil {
				c.shutdown(err)
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *Conn) loop() {
	err := c.readMessages()
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		err = ErrClosed
	}
	c.shutdown(err)
}

func (c *Conn) readMessages() error {
	for {
		t, err := c.r.u8()
		if err != nil {
			return err
		}
		switch t {
		case msgFramebufferUpdate:
			err = c.readUpdate()
		case msgSetColourMap:
			err = c.readColourMap()
		case msgBell:
			c.publish(Event{Kind: EventBell})
		case msgServerCutText:
			err = c.readCutText()
		case msgFence:
			err = c.readFence()
		case msgContinuousUpdates:
			err = c.readEndOfContinuousUpdates()
		default:
			err = protoErr("message", "unknown server message type %d", t)
		}
		if err != nil {
			return err
		}
	}
}

func (c *Conn) readColourMap() error {
	b, err := c.r.bytes(5)
	if err != nil {
		return err
	}
	first, n := int(binary.BigEndian.Uint16(b[1:])), int(binary.BigEndian.Uint16(b[3:]))
	b, err = c.r.bytes(n * 6)
	if err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		if first+i > 255 {
			break
		}
		e := b[i*6:]
		c.colors[first+i] = [3]uint8{e[0], e[2], e[4]}
	}
	return nil
}

func (c *Conn) shutdown(err error) {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		c.nc.Close()
		close(c.done)
		c.log.Debug("rfb closed", "err", err)
	})
}

// Close ends the connection.
func (c *Conn) Close() error {
	c.shutdown(ErrClosed)
	return nil
}

// Done is closed when the connection ends.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err is the reason that the connection ended, or nil.
func (c *Conn) Err() error {
	select {
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.err
	default:
		return nil
	}
}

// Name is the desktop name from ServerInit.
func (c *Conn) Name() string { return c.name }

// Size is the size of the remote screen.
func (c *Conn) Size() image.Point { return c.fb.bounds().Size() }

// PixelFormat is the format on the wire.
func (c *Conn) PixelFormat() PixelFormat {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pf
}

// Facts describe the connection for info and logs.
func (c *Conn) Facts() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := map[string]string{
		"name":         c.name,
		"protocol":     "3." + strconv.Itoa(c.minor),
		"pixel_format": c.pf.String(),
	}
	for k, v := range c.features {
		f[k] = strconv.FormatBool(v)
	}
	return f
}

func (c *Conn) setFeature(name string) {
	c.mu.Lock()
	c.features[name] = true
	c.mu.Unlock()
}

func (c *Conn) hasFeature(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.features[name]
}
