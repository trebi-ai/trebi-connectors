package vnc

import (
	"context"
	"encoding/binary"
	"image"
	"time"
)

// Batch is what a Subscription collects between two calls to Next.
type Batch struct {
	Seq    uint64            // sequence number of the last update
	Rects  []image.Rectangle // changed areas, merged when there are many
	Events []Event
}

// Event kinds.
const (
	EventBell      = "bell"
	EventClipboard = "clipboard"
	EventResize    = "resize"
	EventCursor    = "cursor"
)

// Event is a server event that is not a pixel change.
type Event struct {
	Kind   string
	At     time.Time
	Text   string // clipboard
	Width  int    // resize
	Height int
	Cursor *Cursor // cursor
}

// Cursor is the pointer shape from the server.
type Cursor struct {
	Hash    string
	HotSpot image.Point
	Image   *image.RGBA // alpha 0 where the mask is clear
}

const (
	maxSubRects  = 256
	maxSubEvents = 256
)

// Subscription collects changed rectangles and events. A slow reader never
// stops the decoder: rectangles merge and old events drop.
type Subscription struct {
	c      *Conn
	notify chan struct{}
	batch  Batch
}

// Subscribe starts a new subscription. Close it when done.
func (c *Conn) Subscribe() *Subscription {
	s := &Subscription{c: c, notify: make(chan struct{}, 1)}
	c.mu.Lock()
	c.subs[s] = struct{}{}
	s.batch.Seq = c.seq
	c.mu.Unlock()
	return s
}

// Close stops the subscription.
func (s *Subscription) Close() {
	s.c.mu.Lock()
	delete(s.c.subs, s)
	s.c.mu.Unlock()
}

// Next waits for changes and returns all that came since the last call.
// After the connection ends it returns the remaining changes, then the
// connection error.
func (s *Subscription) Next(ctx context.Context) (Batch, error) {
	for {
		s.c.mu.Lock()
		b := s.batch
		if len(b.Rects) > 0 || len(b.Events) > 0 {
			s.batch = Batch{Seq: b.Seq}
			s.c.mu.Unlock()
			return b, nil
		}
		s.c.mu.Unlock()
		select {
		case <-s.notify:
		case <-s.c.done:
			select {
			case <-s.notify:
				continue
			default:
			}
			return Batch{}, s.c.Err()
		case <-ctx.Done():
			return Batch{}, ctx.Err()
		}
	}
}

func (s *Subscription) wake() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// publish sends an event to each subscription.
func (c *Conn) publish(e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for s := range c.subs {
		if len(s.batch.Events) >= maxSubEvents {
			s.batch.Events = s.batch.Events[1:]
		}
		s.batch.Events = append(s.batch.Events, e)
		s.wake()
	}
}

func (c *Conn) readUpdate() error {
	b, err := c.r.bytes(3)
	if err != nil {
		return err
	}
	n := int(binary.BigEndian.Uint16(b[1:]))
	var rects []image.Rectangle
	for i := 0; i < n; i++ {
		h, err := c.r.bytes(12)
		if err != nil {
			return err
		}
		x, y := int(binary.BigEndian.Uint16(h)), int(binary.BigEndian.Uint16(h[2:]))
		w, ht := int(binary.BigEndian.Uint16(h[4:])), int(binary.BigEndian.Uint16(h[6:]))
		enc := int32(binary.BigEndian.Uint32(h[8:]))
		if enc == EncLastRect {
			break
		}
		d, ok := c.decoders[enc]
		if !ok {
			return protoErr("update", "the server sent encoding %d, which the client did not ask for", enc)
		}
		r := image.Rect(x, y, x+w, y+ht)
		dirty, err := d.decode(c, r)
		if err != nil {
			if _, ok := err.(*ProtocolError); ok {
				return err
			}
			return protoErr("update", "decode %s rectangle %v: %w", encodingName(enc), r, err)
		}
		if dirty && !r.Empty() {
			rects = append(rects, r)
		}
	}
	c.finishUpdate(rects)
	return nil
}

func encodingName(e int32) string {
	for n, v := range EncodingNames {
		if v == e {
			return n
		}
	}
	return "pseudo"
}

func (c *Conn) finishUpdate(rects []image.Rectangle) {
	c.mu.Lock()
	c.seq++
	for s := range c.subs {
		s.batch.Seq = c.seq
		if len(rects) == 0 {
			continue
		}
		s.batch.Rects = append(s.batch.Rects, rects...)
		if len(s.batch.Rects) > maxSubRects {
			u := s.batch.Rects[0]
			for _, r := range s.batch.Rects[1:] {
				u = u.Union(r)
			}
			s.batch.Rects = append(s.batch.Rects[:0], u)
		}
		s.wake()
	}
	for w := range c.waiters {
		if w.update(rects) {
			delete(c.waiters, w)
			close(w.ch)
		}
	}
	c.pending = false
	c.mu.Unlock()
	c.scheduleRequest()
}

// scheduleRequest asks for the next incremental update, no sooner than
// UpdateInterval after the last request.
func (c *Conn) scheduleRequest() {
	iv := c.opts.UpdateInterval
	if iv <= 0 {
		iv = 50 * time.Millisecond
	}
	c.mu.Lock()
	wait := time.Until(c.lastReq.Add(iv))
	c.mu.Unlock()
	// Not in the read goroutine: a write can wait for the server to read.
	time.AfterFunc(max(wait, 0), func() { c.requestUpdate(true) })
}

// requestUpdate sends a FramebufferUpdateRequest for the whole screen. An
// incremental request is not sent when one is already out.
func (c *Conn) requestUpdate(incremental bool) error {
	if err := c.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	// Continuous updates need no incremental requests.
	if incremental && (c.pending || c.continuous) {
		c.mu.Unlock()
		return nil
	}
	c.pending = true
	c.lastReq = time.Now()
	c.mu.Unlock()
	return c.requestArea(c.fb.bounds(), incremental)
}

func (c *Conn) requestArea(r image.Rectangle, incremental bool) error {
	b := make([]byte, 10)
	b[0] = msgFramebufferRequest
	if incremental {
		b[1] = 1
	}
	binary.BigEndian.PutUint16(b[2:], uint16(r.Min.X))
	binary.BigEndian.PutUint16(b[4:], uint16(r.Min.Y))
	binary.BigEndian.PutUint16(b[6:], uint16(r.Dx()))
	binary.BigEndian.PutUint16(b[8:], uint16(r.Dy()))
	return c.write(b)
}

// resize changes the framebuffer size, as DesktopSize asks.
func (c *Conn) resize(w, h int) {
	if c.fb.bounds().Size() == image.Pt(w, h) {
		return
	}
	c.fb.resize(w, h)
	c.mu.Lock()
	for wt := range c.waiters {
		if wt.cov != nil {
			wt.cov = newCoverage(image.Rect(0, 0, w, h))
		}
	}
	continuous := c.continuous
	c.mu.Unlock()
	if continuous {
		c.reply(continuousMessage(true, image.Rect(0, 0, w, h)))
	}
	c.publish(Event{Kind: EventResize, Width: w, Height: h})
}

// waiter waits for an update. With cov, it waits until the updates cover
// the area; without, for the end of the next update.
type waiter struct {
	cov *coverage
	ch  chan struct{}
}

func (w *waiter) update(rects []image.Rectangle) bool {
	if w.cov == nil {
		return true
	}
	for _, r := range rects {
		w.cov.add(r)
	}
	return w.cov.left == 0
}

// coverage counts the pixels of an area that no rectangle covered yet.
type coverage struct {
	area image.Rectangle
	mask []bool
	left int
}

func newCoverage(area image.Rectangle) *coverage {
	return &coverage{area: area, mask: make([]bool, area.Dx()*area.Dy()), left: area.Dx() * area.Dy()}
}

func (cv *coverage) add(r image.Rectangle) {
	r = r.Intersect(cv.area)
	w := cv.area.Dx()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := cv.mask[(y-cv.area.Min.Y)*w:]
		for x := r.Min.X - cv.area.Min.X; x < r.Max.X-cv.area.Min.X; x++ {
			if !row[x] {
				row[x] = true
				cv.left--
			}
		}
	}
}

// Refresh asks for an update and waits for it. With full, it asks for the
// whole screen and waits until the server has sent all of it. Without, it
// waits for the next incremental update.
func (c *Conn) Refresh(ctx context.Context, full bool) error {
	if full {
		// The first full update is already on its way.
		select {
		case <-c.first.ch:
		default:
			return c.wait(ctx, c.first)
		}
	}
	w := &waiter{ch: make(chan struct{})}
	if full {
		w.cov = newCoverage(c.fb.bounds())
	}
	c.mu.Lock()
	c.waiters[w] = struct{}{}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.waiters, w)
		c.mu.Unlock()
	}()
	var err error
	if full {
		err = c.requestArea(c.fb.bounds(), false)
	} else {
		err = c.requestUpdate(true)
	}
	if err != nil {
		return err
	}
	return c.wait(ctx, w)
}

func (c *Conn) wait(ctx context.Context, w *waiter) error {
	select {
	case <-w.ch:
		return nil
	case <-c.done:
		return c.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Seq is the number of updates so far.
func (c *Conn) Seq() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seq
}

// Snapshot copies the area r of the screen. An empty r means the whole
// screen. Options.Cursor selects whether the pointer is in it; see
// SnapshotCursor.
func (c *Conn) Snapshot(r image.Rectangle) *image.RGBA {
	return c.SnapshotCursor(r, c.opts.Cursor)
}
