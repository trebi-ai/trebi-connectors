// Package vnctest is a fake RFB server for tests and for serve --sandbox.
package vnctest

import (
	"bufio"
	"context"
	"crypto/des"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math/bits"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
)

// Options configure the server.
type Options struct {
	Width, Height int    // default 64x48
	Name          string // default "vnctest"
	Version       string // "3.3", "3.7", or "3.8" (default)
	// Format is the native pixel format. The default is bgrx8888.
	Format *vnc.PixelFormat
	// Security lists the offered types; the default is None. On 3.3 the
	// first one is used.
	Security []uint8
	Password string
	// User is the user for Apple Remote Desktop and VeNCrypt Plain.
	User string
	// VeNCrypt lists the offered subtypes. The default is the X509 ones.
	VeNCrypt []uint32
	// TLS is the server certificate for VeNCrypt (see CA).
	TLS *tls.Config
	// FailReason is the reason that a failed login gives on 3.8.
	FailReason string
	Ext        Extensions
}

// InputEvent is one input message from a client.
type InputEvent struct {
	Kind    string // key, pointer, or cut
	Keysym  uint32
	Down    bool
	X, Y    int
	Buttons uint8
	Text    string
	// Scancode is the XT scan code of a QEMU key event.
	Scancode uint32
}

// Server is a fake RFB server with a screen in RGBA.
type Server struct {
	opts Options

	mu      sync.Mutex
	changed chan struct{}
	screen  *image.RGBA
	events  []InputEvent
	clients map[*client]struct{}
	conns   map[net.Conn]struct{}
	served  int
	ln      net.Listener
	wg      sync.WaitGroup
	x       serverExt
}

// New makes a server with a black screen.
func New(o Options) *Server {
	if o.Width == 0 {
		o.Width, o.Height = 64, 48
	}
	if o.Name == "" {
		o.Name = "vnctest"
	}
	if o.Version == "" {
		o.Version = "3.8"
	}
	if o.Format == nil {
		f := vnc.FormatBGRX8888
		o.Format = &f
	}
	if o.Security == nil {
		o.Security = []uint8{vnc.SecNone}
	}
	s := &Server{
		opts:    o,
		changed: make(chan struct{}),
		screen:  image.NewRGBA(image.Rect(0, 0, o.Width, o.Height)),
		clients: map[*client]struct{}{},
		conns:   map[net.Conn]struct{}{},
	}
	draw.Draw(s.screen, s.screen.Rect, image.NewUniform(color.RGBA{0, 0, 0, 255}), image.Point{}, draw.Src)
	return s
}

// Listen accepts clients on 127.0.0.1 and returns the address in the
// host::port form.
func (s *Server) Listen() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	return s.ListenOn(ln), nil
}

// ListenOn accepts clients on ln and returns its address in the host::port
// form for a TCP listener.
func (s *Server) ListenOn(ln net.Listener) string {
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	s.wg.Go(func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			s.wg.Go(func() { s.Serve(nc) })
		}
	})
	if a, ok := ln.Addr().(*net.TCPAddr); ok {
		return fmt.Sprintf("%s::%d", a.IP, a.Port)
	}
	return ln.Addr().String()
}

// Pipe starts a client session over net.Pipe and returns the client end.
func (s *Server) Pipe() net.Conn {
	a, b := net.Pipe()
	s.wg.Go(func() { s.Serve(b) })
	return a
}

// Close stops the listener and ends all sessions.
func (s *Server) Close() {
	s.mu.Lock()
	if s.ln != nil {
		s.ln.Close()
	}
	for nc := range s.conns {
		nc.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// Served is the number of client connections so far.
func (s *Server) Served() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.served
}

// Drop closes all client connections and keeps the listener.
func (s *Server) Drop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for nc := range s.conns {
		nc.Close()
	}
}

// Screen returns a copy of the server screen.
func (s *Server) Screen() *image.RGBA {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := image.NewRGBA(s.screen.Rect)
	copy(out.Pix, s.screen.Pix)
	return out
}

// Paint fills r with col and sends the change to the clients.
func (s *Server) Paint(r image.Rectangle, col color.RGBA) {
	s.mu.Lock()
	draw.Draw(s.screen, r, image.NewUniform(col), image.Point{}, draw.Src)
	r = r.Intersect(s.screen.Rect)
	for c := range s.clients {
		c.dirty = append(c.dirty, r)
	}
	s.mu.Unlock()
	s.kick()
}

// Draw copies img to the screen at p.
func (s *Server) Draw(p image.Point, img image.Image) {
	s.mu.Lock()
	r := img.Bounds().Sub(img.Bounds().Min).Add(p)
	draw.Draw(s.screen, r, img, img.Bounds().Min, draw.Src)
	r = r.Intersect(s.screen.Rect)
	for c := range s.clients {
		c.dirty = append(c.dirty, r)
	}
	s.mu.Unlock()
	s.kick()
}

// Resize changes the screen size. Clients that offered DesktopSize get it.
func (s *Server) Resize(w, h int) {
	s.mu.Lock()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Rect, s.screen, image.Point{}, draw.Src)
	s.screen = img
	for c := range s.clients {
		c.resized = true
		c.dirty = append(c.dirty[:0], img.Rect)
	}
	s.mu.Unlock()
	s.kick()
}

// Bell rings the bell of each client.
func (s *Server) Bell() { s.SendRaw([]byte{2}) }

// CutText sends Latin-1 clipboard text to each client.
func (s *Server) CutText(text string) {
	var data []byte
	for _, r := range text {
		data = append(data, byte(r))
	}
	b := make([]byte, 8, 8+len(data))
	b[0] = 3
	binary.BigEndian.PutUint32(b[4:], uint32(len(data)))
	s.SendRaw(append(b, data...))
}

// SendRaw writes bytes to each client as they are.
func (s *Server) SendRaw(b []byte) {
	s.mu.Lock()
	cs := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		cs = append(cs, c)
	}
	s.mu.Unlock()
	for _, c := range cs {
		c.write(b)
	}
}

// Events returns the input events so far.
func (s *Server) Events() []InputEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

// WaitEvents waits until there are at least n input events.
func (s *Server) WaitEvents(ctx context.Context, n int) ([]InputEvent, error) {
	for {
		s.mu.Lock()
		ev, ch := slices.Clone(s.events), s.changed
		s.mu.Unlock()
		if len(ev) >= n {
			return ev, nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ev, fmt.Errorf("got %d of %d input events: %w", len(ev), n, ctx.Err())
		}
	}
}

// Encodings is the last encoding list from a client.
func (s *Server) Encodings() []int32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		return slices.Clone(c.encs)
	}
	return nil
}

// ClientFormat is the pixel format that the last client set.
func (s *Server) ClientFormat() vnc.PixelFormat {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		return c.pf
	}
	return *s.opts.Format
}

// Shared is the shared flag of the last ClientInit.
func (s *Server) Shared() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		return c.shared
	}
	return false
}

func (s *Server) record(e InputEvent) {
	s.mu.Lock()
	s.events = append(s.events, e)
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

func (s *Server) kick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
}

type client struct {
	s    *Server
	nc   net.Conn
	r    *bufio.Reader
	wmu  sync.Mutex
	wake chan struct{}

	// Under s.mu.
	pf      vnc.PixelFormat
	encs    []int32
	shared  bool
	want    bool
	resized bool
	dirty   []image.Rectangle
	x       clientExt
}

func (c *client) write(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.nc.Write(b)
	return err
}

// Serve runs one client session until it ends.
func (s *Server) Serve(nc net.Conn) error {
	s.mu.Lock()
	s.conns[nc] = struct{}{}
	s.served++
	s.mu.Unlock()
	defer func() {
		nc.Close()
		s.mu.Lock()
		delete(s.conns, nc)
		s.mu.Unlock()
	}()
	c := &client{s: s, nc: nc, r: bufio.NewReader(nc), wake: make(chan struct{}, 1), pf: *s.opts.Format}
	if err := c.handshake(); err != nil {
		return err
	}
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
	}()
	done := make(chan struct{})
	defer close(done)
	go c.sender(done)
	return c.readLoop()
}

func (c *client) read(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := io.ReadFull(c.r, b)
	return b, err
}

func (c *client) handshake() error {
	o := c.s.opts
	minor := map[string]int{"3.3": 3, "3.7": 7, "3.8": 8}[o.Version]
	if err := c.write(fmt.Appendf(nil, "RFB 003.%03d\n", minor)); err != nil {
		return err
	}
	if _, err := c.read(12); err != nil {
		return err
	}
	var sec uint8
	if minor == 3 {
		sec = o.Security[0]
		if err := c.write(binary.BigEndian.AppendUint32(nil, uint32(sec))); err != nil {
			return err
		}
	} else {
		if err := c.write(append([]byte{byte(len(o.Security))}, o.Security...)); err != nil {
			return err
		}
		b, err := c.read(1)
		if err != nil {
			return err
		}
		sec = b[0]
		if !slices.Contains(o.Security, sec) {
			return fmt.Errorf("client chose type %d, which was not offered", sec)
		}
	}
	ok := true
	switch sec {
	case vnc.SecNone:
		if minor < 8 {
			break
		}
		if err := c.write(make([]byte, 4)); err != nil {
			return err
		}
	case vnc.SecVNC:
		var err error
		if ok, err = c.vncAuth(); err != nil {
			return err
		}
		if err := c.result(ok, minor); err != nil {
			return err
		}
	case vnc.SecAppleARD, vnc.SecVeNCrypt:
		var err error
		if sec == vnc.SecAppleARD {
			ok, err = c.ard()
		} else {
			ok, err = c.veNCrypt()
		}
		if err != nil {
			return err
		}
		if err := c.result(ok, minor); err != nil {
			return err
		}
	default:
		return fmt.Errorf("security type %d is not in vnctest", sec)
	}
	if !ok {
		return errors.New("login failed")
	}
	b, err := c.read(1)
	if err != nil {
		return err
	}
	c.s.mu.Lock()
	c.shared = b[0] != 0
	w, h := c.s.screen.Rect.Dx(), c.s.screen.Rect.Dy()
	c.s.mu.Unlock()
	si := make([]byte, 0, 24+len(o.Name))
	si = binary.BigEndian.AppendUint16(si, uint16(w))
	si = binary.BigEndian.AppendUint16(si, uint16(h))
	si = append(si, o.Format.Bytes()...)
	si = binary.BigEndian.AppendUint32(si, uint32(len(o.Name)))
	si = append(si, o.Name...)
	return c.write(si)
}

func (c *client) result(ok bool, minor int) error {
	if ok {
		return c.write(make([]byte, 4))
	}
	b := binary.BigEndian.AppendUint32(nil, 1)
	if minor >= 8 {
		reason := c.s.opts.FailReason
		if reason == "" {
			reason = "Authentication failed"
		}
		b = binary.BigEndian.AppendUint32(b, uint32(len(reason)))
		b = append(b, reason...)
	}
	return c.write(b)
}

func desResponse(password string, challenge []byte) []byte {
	key := make([]byte, 8)
	copy(key, password)
	for i, k := range key {
		key[i] = bits.Reverse8(k)
	}
	block, _ := des.NewCipher(key)
	out := make([]byte, 16)
	block.Encrypt(out, challenge)
	block.Encrypt(out[8:], challenge[8:])
	return out
}

func (c *client) readLoop() error {
	for {
		t, err := c.r.ReadByte()
		if err != nil {
			return err
		}
		switch t {
		case 0:
			b, err := c.read(19)
			if err != nil {
				return err
			}
			c.s.mu.Lock()
			c.pf = vnc.ParsePixelFormat(b[3:])
			c.s.mu.Unlock()
		case 2:
			b, err := c.read(3)
			if err != nil {
				return err
			}
			n := int(binary.BigEndian.Uint16(b[1:]))
			b, err = c.read(4 * n)
			if err != nil {
				return err
			}
			encs := make([]int32, n)
			for i := range encs {
				encs[i] = int32(binary.BigEndian.Uint32(b[4*i:]))
			}
			c.s.mu.Lock()
			c.encs = encs
			c.s.mu.Unlock()
			if err := c.encodingsSet(); err != nil {
				return err
			}
		case 3:
			b, err := c.read(9)
			if err != nil {
				return err
			}
			r := image.Rect(int(binary.BigEndian.Uint16(b[1:])), int(binary.BigEndian.Uint16(b[3:])), 0, 0)
			r.Max = r.Min.Add(image.Pt(int(binary.BigEndian.Uint16(b[5:])), int(binary.BigEndian.Uint16(b[7:]))))
			c.s.mu.Lock()
			c.want = true
			if b[0] == 0 {
				c.dirty = append(c.dirty, r.Intersect(c.s.screen.Rect))
			}
			c.s.mu.Unlock()
			select {
			case c.wake <- struct{}{}:
			default:
			}
		case 4:
			b, err := c.read(7)
			if err != nil {
				return err
			}
			c.s.record(InputEvent{Kind: "key", Down: b[0] != 0, Keysym: binary.BigEndian.Uint32(b[3:])})
		case 5:
			b, err := c.read(5)
			if err != nil {
				return err
			}
			c.s.record(InputEvent{Kind: "pointer", Buttons: b[0],
				X: int(binary.BigEndian.Uint16(b[1:])), Y: int(binary.BigEndian.Uint16(b[3:]))})
		case 6:
			b, err := c.read(7)
			if err != nil {
				return err
			}
			if n := int32(binary.BigEndian.Uint32(b[3:])); n < 0 {
				if err := c.readExtClipboard(int(-n)); err != nil {
					return err
				}
				continue
			}
			b, err = c.read(int(binary.BigEndian.Uint32(b[3:])))
			if err != nil {
				return err
			}
			r := make([]rune, len(b))
			for i, x := range b {
				r[i] = rune(x)
			}
			c.s.record(InputEvent{Kind: "cut", Text: string(r)})
		default:
			if err := c.readExt(t); err != nil {
				return err
			}
		}
	}
}

// sender sends an update when the client wants one and the screen changed.
func (c *client) sender(done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-c.wake:
		}
		c.s.mu.Lock()
		if !c.want || (len(c.dirty) == 0 && !c.resized && !c.x.pending()) {
			c.s.mu.Unlock()
			continue
		}
		msg := c.encodeUpdate()
		c.want, c.resized, c.dirty = c.x.continuous, false, nil
		c.s.mu.Unlock()
		if c.write(msg) != nil {
			return
		}
	}
}

// encodeUpdate builds a FramebufferUpdate in the first encoding of the
// client that vnctest has (encode.go). It runs under s.mu.
func (c *client) encodeUpdate() []byte {
	rects := append(c.resizeRects(), c.copyRects()...)
	for _, r := range c.dirty {
		if r.Empty() {
			continue
		}
		rects = append(rects, c.encodeRect(r)...)
	}
	rects = append(rects, c.pseudoRects()...)
	msg := []byte{0, 0}
	msg = binary.BigEndian.AppendUint16(msg, uint16(len(rects)))
	for _, r := range rects {
		msg = append(msg, r...)
	}
	return msg
}

func rectHeader(r image.Rectangle, enc int32) []byte {
	b := make([]byte, 0, 12)
	b = binary.BigEndian.AppendUint16(b, uint16(r.Min.X))
	b = binary.BigEndian.AppendUint16(b, uint16(r.Min.Y))
	b = binary.BigEndian.AppendUint16(b, uint16(r.Dx()))
	b = binary.BigEndian.AppendUint16(b, uint16(r.Dy()))
	return binary.BigEndian.AppendUint32(b, uint32(enc))
}

// pixels encodes the screen area r in the client pixel format.
func (c *client) pixels(r image.Rectangle) []byte {
	bpp := int(c.pf.BPP) / 8
	out := make([]byte, 0, r.Dx()*r.Dy()*bpp)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			out = append(out, Pack(c.pf, c.s.screen.RGBAAt(x, y))...)
		}
	}
	return out
}

// Pack encodes one colour as a PIXEL in pf (true colour only).
func Pack(pf vnc.PixelFormat, col color.RGBA) []byte {
	scale := func(v uint8, max uint16) uint32 { return (uint32(v)*uint32(max) + 127) / 255 }
	v := scale(col.R, pf.RedMax)<<pf.RedShift | scale(col.G, pf.GreenMax)<<pf.GreenShift | scale(col.B, pf.BlueMax)<<pf.BlueShift
	n := int(pf.BPP) / 8
	b := make([]byte, n)
	for i := 0; i < n; i++ {
		shift := 8 * i
		if pf.BigEndian {
			shift = 8 * (n - 1 - i)
		}
		b[i] = byte(v >> shift)
	}
	return b
}

// Animate paints one scripted change every interval until ctx ends: a
// block that moves across the screen.
func (s *Server) Animate(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	colors := []color.RGBA{{220, 40, 40, 255}, {40, 160, 60, 255}, {40, 80, 220, 255}}
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.mu.Lock()
		w, h := s.screen.Rect.Dx(), s.screen.Rect.Dy()
		s.mu.Unlock()
		size := max(min(w, h)/4, 1)
		x := (i * size) % max(w-size+1, 1)
		s.Paint(image.Rect(0, 0, w, h), color.RGBA{0, 0, 0, 255})
		s.Paint(image.Rect(x, h/2-size/2, x+size, h/2+size/2), colors[i%len(colors)])
	}
}
