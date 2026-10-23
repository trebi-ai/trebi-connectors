package vnc

import (
	"encoding/binary"
	"image"
)

// qemuKeyDecoder reads the QEMU extended key pseudo-encoding. The server
// sends it once to say that it takes key events with a scan code.
type qemuKeyDecoder struct{}

func (qemuKeyDecoder) decode(c *Conn, r image.Rectangle) (bool, error) {
	c.setFeature("qemu_keys")
	return false, nil
}

const (
	msgQEMU    = 255
	qemuKeyMsg = 0
)

// scanCodes maps the keysyms whose key does not depend on the keyboard
// layout to XT scan codes. A code with 0x80 set is 0xE0 followed by the code
// without 0x80. Characters are not in the map: their key depends on the
// layout of the guest.
var scanCodes = map[uint32]uint32{
	KeyEscape: 0x01, KeyBackSpace: 0x0e, KeyTab: 0x0f, KeyReturn: 0x1c, ' ': 0x39,
	KeyControlL: 0x1d, KeyShiftL: 0x2a, KeyShiftR: 0x36, KeyAltL: 0x38, KeyCapsLock: 0x3a,
	KeyNumLock: 0x45, KeyScrollLock: 0x46,
	KeyF1: 0x3b, KeyF1 + 1: 0x3c, KeyF1 + 2: 0x3d, KeyF1 + 3: 0x3e, KeyF1 + 4: 0x3f,
	KeyF1 + 5: 0x40, KeyF1 + 6: 0x41, KeyF1 + 7: 0x42, KeyF1 + 8: 0x43, KeyF1 + 9: 0x44,
	KeyF1 + 10: 0x57, KeyF1 + 11: 0x58,
	KeyKP0 + 7: 0x47, KeyKP0 + 8: 0x48, KeyKP0 + 9: 0x49, KeyKP0 + 4: 0x4b, KeyKP0 + 5: 0x4c,
	KeyKP0 + 6: 0x4d, KeyKP0 + 1: 0x4f, KeyKP0 + 2: 0x50, KeyKP0 + 3: 0x51, KeyKP0: 0x52,
	KeyKPEnter: 0x9c, KeyControlR: 0x9d, KeyAltR: 0xb8, KeySysReq: 0xb7,
	KeyHome: 0xc7, KeyUp: 0xc8, KeyPageUp: 0xc9, KeyLeft: 0xcb, KeyRight: 0xcd,
	KeyEnd: 0xcf, KeyDown: 0xd0, KeyPageDown: 0xd1, KeyInsert: 0xd2, KeyDelete: 0xd3,
	KeySuperL: 0xdb, KeySuperR: 0xdc, KeyMenu: 0xdd,
}

// qemuKey is the QEMU extended key event, or nil when the server does not
// take it or the key has no known scan code.
func (c *Conn) qemuKey(keysym uint32, down bool) []byte {
	code, ok := scanCodes[keysym]
	if !ok || !c.hasFeature("qemu_keys") {
		return nil
	}
	b := make([]byte, 12)
	b[0], b[1] = msgQEMU, qemuKeyMsg
	if down {
		b[3] = 1
	}
	binary.BigEndian.PutUint32(b[4:], keysym)
	binary.BigEndian.PutUint32(b[8:], code)
	return b
}
