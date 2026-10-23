package vnc

import (
	"context"
	"encoding/binary"
	"fmt"
	"image"
)

// Fence and continuous updates (rfbproto). The two message types are the
// same in both directions.
const (
	msgFence             = 248
	msgContinuousUpdates = 150

	fenceBlockBefore = 1 << 0
	fenceBlockAfter  = 1 << 1
	fenceSyncNext    = 1 << 2
	fenceRequest     = 1 << 31
	fenceKnown       = fenceBlockBefore | fenceBlockAfter | fenceSyncNext

	maxFencePayload = 64
)

func fenceMessage(flags uint32, payload []byte) []byte {
	b := make([]byte, 9, 9+len(payload))
	b[0] = msgFence
	binary.BigEndian.PutUint32(b[4:], flags)
	b[8] = byte(len(payload))
	return append(b, payload...)
}

// readFence answers a fence request from the server, or ends a Sync.
func (c *Conn) readFence() error {
	b, err := c.r.bytes(8)
	if err != nil {
		return err
	}
	flags, n := binary.BigEndian.Uint32(b[3:]), int(b[7])
	if n > maxFencePayload {
		return protoErr("fence", "payload of %d bytes is too long", n)
	}
	payload, err := c.r.bytes(n)
	if err != nil {
		return err
	}
	c.setFeature("fence")
	if flags&fenceRequest != 0 {
		// Each earlier message is already handled, so the answer can go.
		c.reply(fenceMessage(flags&fenceKnown, payload))
		return nil
	}
	if n == 4 {
		id := binary.BigEndian.Uint32(payload)
		c.mu.Lock()
		if ch, ok := c.fences[id]; ok {
			close(ch)
			delete(c.fences, id)
		}
		c.mu.Unlock()
	}
	return nil
}

// Sync waits until the server has handled each message that the client
// sent before it. It needs the Fence extension.
func (c *Conn) Sync(ctx context.Context) error {
	if !c.hasFeature("fence") {
		return ErrUnsupported
	}
	ch := make(chan struct{})
	c.mu.Lock()
	c.fenceID++
	id := c.fenceID
	c.fences[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.fences, id)
		c.mu.Unlock()
	}()
	if err := c.write(fenceMessage(fenceRequest|fenceBlockBefore, binary.BigEndian.AppendUint32(nil, id))); err != nil {
		return err
	}
	select {
	case <-ch:
		return nil
	case <-c.done:
		return c.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// readEndOfContinuousUpdates handles EndOfContinuousUpdates. The server
// sends it once to say that it supports continuous updates, and again
// when they stop.
func (c *Conn) readEndOfContinuousUpdates() error {
	c.mu.Lock()
	c.features["continuous_updates"] = true
	c.continuous = false
	c.mu.Unlock()
	c.scheduleRequest()
	return nil
}

func continuousMessage(on bool, r image.Rectangle) []byte {
	b := make([]byte, 10)
	b[0] = msgContinuousUpdates
	if on {
		b[1] = 1
	}
	binary.BigEndian.PutUint16(b[2:], uint16(r.Min.X))
	binary.BigEndian.PutUint16(b[4:], uint16(r.Min.Y))
	binary.BigEndian.PutUint16(b[6:], uint16(r.Dx()))
	binary.BigEndian.PutUint16(b[8:], uint16(r.Dy()))
	return b
}

// EnableContinuousUpdates asks the server to send each change without a
// request, or to stop. It needs the ContinuousUpdates extension.
func (c *Conn) EnableContinuousUpdates(on bool) error {
	if !c.hasFeature("continuous_updates") {
		return ErrUnsupported
	}
	c.mu.Lock()
	if on {
		c.continuous = true
	}
	c.mu.Unlock()
	if err := c.write(continuousMessage(on, c.fb.bounds())); err != nil {
		return fmt.Errorf("continuous updates: %w", err)
	}
	return nil
}

// ContinuousUpdates is true while the server sends updates without a
// request.
func (c *Conn) ContinuousUpdates() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.continuous
}
