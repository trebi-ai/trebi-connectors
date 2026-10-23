package vnc

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"strings"
)

// Extended clipboard flags (rfbproto ExtendedClipboard). The low 16 bits
// are formats; the client handles text only.
const (
	clipText    = 1 << 0
	clipCaps    = 1 << 24
	clipRequest = 1 << 25
	clipPeek    = 1 << 26
	clipNotify  = 1 << 27
	clipProvide = 1 << 28

	clipFormats = 0xffff
)

// extClipboardMessage is a ClientCutText with a negative length.
func extClipboardMessage(flags uint32, payload []byte) []byte {
	b := make([]byte, 12, 12+len(payload))
	b[0] = msgClientCutText
	binary.BigEndian.PutUint32(b[4:], uint32(-int32(4+len(payload))))
	binary.BigEndian.PutUint32(b[8:], flags)
	return append(b, payload...)
}

// clipData is text as UTF-8 with CRLF line ends and a NUL.
func clipData(text string) []byte {
	return append([]byte(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")), 0)
}

// provideMessage holds the data in a zlib stream of its own.
func provideMessage(data []byte) []byte {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(data)))
	zw.Write(n[:])
	zw.Write(data)
	zw.Close()
	return extClipboardMessage(clipProvide|clipText, buf.Bytes())
}

// readExtClipboard reads an extended clipboard message of n bytes.
func (c *Conn) readExtClipboard(n int) error {
	if n < 4 {
		return protoErr("clipboard", "extended clipboard message of %d bytes is too short", n)
	}
	b, err := c.r.bytes(n)
	if err != nil {
		return err
	}
	flags, payload := binary.BigEndian.Uint32(b), b[4:]
	switch {
	case flags&clipCaps != 0:
		// One size for each format bit, from bit 0 up.
		if len(payload) < 4*bits.OnesCount32(flags&clipFormats) {
			return protoErr("clipboard", "the capabilities are too short")
		}
		var limit uint32
		if flags&clipText != 0 {
			limit = binary.BigEndian.Uint32(payload)
		}
		c.mu.Lock()
		c.features["extended_clipboard"] = true
		c.clipFlags, c.clipLimit = flags, limit
		c.mu.Unlock()
		c.reply(extClipboardMessage(clipCaps|clipRequest|clipPeek|clipNotify|clipProvide|clipText,
			binary.BigEndian.AppendUint32(nil, maxClipboard)))
	case flags&clipRequest != 0:
		c.mu.Lock()
		text, ok := c.clipOut, c.clipOwner
		c.mu.Unlock()
		if ok && flags&clipText != 0 {
			c.reply(provideMessage(clipData(text)))
		}
	case flags&clipPeek != 0:
		c.mu.Lock()
		var f uint32 = clipNotify
		if c.clipOwner {
			f |= clipText
		}
		c.mu.Unlock()
		c.reply(extClipboardMessage(f, nil))
	case flags&clipNotify != 0:
		if flags&clipText != 0 && c.serverClip(clipRequest) {
			c.reply(extClipboardMessage(clipRequest|clipText, nil))
		}
	case flags&clipProvide != 0:
		if flags&clipText == 0 {
			return nil
		}
		text, err := readProvidedText(payload)
		if err != nil {
			return protoErr("clipboard", "%v", err)
		}
		c.setClipboard(text)
	}
	return nil
}

// readProvidedText reads the text of a provide message. Text is the first
// format, so the other formats after it are not read.
func readProvidedText(payload []byte) (string, error) {
	zr, err := zlib.NewReader(bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("zlib: %w", err)
	}
	var n [4]byte
	if _, err := io.ReadFull(zr, n[:]); err != nil {
		return "", errors.New("the clipboard data is too short")
	}
	size := binary.BigEndian.Uint32(n[:])
	if size > maxClipboard {
		return "", fmt.Errorf("text of %d bytes is too long", size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(zr, data); err != nil {
		return "", errors.New("the clipboard data is too short")
	}
	data, _ = bytes.CutSuffix(data, []byte{0})
	return strings.ReplaceAll(string(data), "\r\n", "\n"), nil
}

// serverClip is true when the server takes the clipboard action.
func (c *Conn) serverClip(action uint32) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clipFlags&action != 0
}

// setExtClipboard announces new text to the server: it sends the text
// when the server takes it and the size fits, else a notify. The server
// can then ask for the text.
func (c *Conn) setExtClipboard(text string) error {
	c.mu.Lock()
	c.clipOut, c.clipOwner = text, true
	flags, limit := c.clipFlags, c.clipLimit
	c.mu.Unlock()
	if data := clipData(text); flags&clipProvide != 0 && uint64(len(data)) <= uint64(limit) {
		return c.write(provideMessage(data))
	}
	if flags&clipNotify != 0 {
		return c.write(extClipboardMessage(clipNotify|clipText, nil))
	}
	return nil
}
