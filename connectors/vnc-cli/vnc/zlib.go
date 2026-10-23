package vnc

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
)

// zstream is a zlib stream that lasts for the whole connection. The server
// flushes it at the end of each rectangle. A decoder must read exactly the
// bytes that the rectangle needs, because flate fails for good when its
// input ends.
type zstream struct {
	in  bytes.Buffer
	zr  io.ReadCloser
	one [1]byte
}

// maxZlibLeft bounds the compressed bytes that a rectangle leaves unread.
// A sync flush leaves a few.
const maxZlibLeft = 1024

// feed adds the compressed bytes of the next rectangle.
func (z *zstream) feed(b []byte) error {
	if z.in.Len() > maxZlibLeft {
		return fmt.Errorf("the last rectangle left %d compressed bytes", z.in.Len())
	}
	z.in.Write(b)
	return nil
}

func (z *zstream) reset() {
	z.in.Reset()
	z.zr = nil
}

func (z *zstream) read(p []byte) error {
	if z.zr == nil {
		zr, err := zlib.NewReader(&z.in)
		if err != nil {
			return fmt.Errorf("zlib: %w", err)
		}
		z.zr = zr
	}
	_, err := io.ReadFull(z.zr, p)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.New("the compressed data is too short")
	}
	return err
}

func (z *zstream) u8() (uint8, error) {
	err := z.read(z.one[:])
	return z.one[0], err
}
