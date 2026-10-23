package vnc

import (
	"bufio"
	"crypto/des"
	"encoding/binary"
	"fmt"
	"io"
	"math/bits"
	"net"
	"slices"
	"strings"
)

// Security type numbers (RFC 6143 §7.2, rfbproto).
const (
	SecNone      uint8 = 1
	SecVNC       uint8 = 2
	SecVeNCrypt  uint8 = 19
	SecAppleARD  uint8 = 30
	maxReasonLen       = 1 << 16
)

var securityNames = map[uint8]string{
	1: "None", 2: "VNC password", 5: "RSA-AES", 6: "RSA-AES unencrypted", 13: "RSA-AES two step",
	16: "Tight", 17: "Ultra", 18: "TLS", 19: "VeNCrypt", 20: "SASL", 30: "Apple Remote Desktop",
	129: "RSA-AES-256", 130: "RSA-AES-256 unencrypted",
}

func securityName(t uint8) string {
	if n, ok := securityNames[t]; ok {
		return fmt.Sprintf("%s (%d)", n, t)
	}
	return fmt.Sprintf("type %d", t)
}

// securityType is one RFB security type. Handshake runs after the type is
// chosen and returns when the SecurityResult (if any) is read.
type securityType interface {
	handshake(h *handshake) error
}

// securityTypes holds the types that this client implements.
var securityTypes = map[uint8]func() securityType{
	SecNone: func() securityType { return secNone{} },
	SecVNC:  func() securityType { return secVNC{} },
}

// handshake is the state of the connection before ServerInit. A security
// type that wraps the transport (TLS) replaces conn and r.
type handshake struct {
	conn  net.Conn
	r     *bufio.Reader
	minor int // 3, 7, or 8
	opts  *Options
}

func (h *handshake) read(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := io.ReadFull(h.r, b)
	return b, err
}

func (h *handshake) u32() (uint32, error) {
	b, err := h.read(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

func (h *handshake) write(b []byte) error {
	_, err := h.conn.Write(b)
	return err
}

// reason reads a u32 length and a string.
func (h *handshake) reason() (string, error) {
	n, err := h.u32()
	if err != nil {
		return "", err
	}
	if n > maxReasonLen {
		return "", fmt.Errorf("reason of %d bytes is too long", n)
	}
	b, err := h.read(int(n))
	return strings.TrimSpace(string(b)), err
}

// result reads the SecurityResult (RFC 6143 §7.1.3).
func (h *handshake) result() error {
	v, err := h.u32()
	if err != nil {
		return err
	}
	switch v {
	case 0:
		return nil
	case 1, 2:
		reason := "wrong password or user"
		if v == 2 {
			reason = "too many tries"
		}
		if h.minor >= 8 {
			if s, err := h.reason(); err == nil && s != "" {
				reason = s
			}
		}
		return &AuthError{Reason: reason}
	default:
		return protoErr("security result", "unknown value %d", v)
	}
}

// chooseSecurity picks the first type in the preference order that the
// server offers and that the client implements.
func chooseSecurity(offered []uint8, o *Options) (uint8, error) {
	for _, t := range o.securityOrder() {
		if _, ok := securityTypes[t]; ok && slices.Contains(offered, t) {
			return t, nil
		}
	}
	names := make([]string, len(offered))
	for i, t := range offered {
		names[i] = securityName(t)
	}
	return 0, protoErr("security", "no security type in common: the server offers %s", strings.Join(names, ", "))
}

type secNone struct{}

func (secNone) handshake(h *handshake) error {
	// rfbproto: a SecurityResult follows None only from 3.8.
	if h.minor < 8 {
		return nil
	}
	return h.result()
}

type secVNC struct{}

func (secVNC) handshake(h *handshake) error {
	challenge, err := h.read(16)
	if err != nil {
		return err
	}
	if h.opts.Password == "" {
		return &AuthError{Reason: "the computer asks for a password, and none is set"}
	}
	resp, err := vncAuthResponse(h.opts.Password, challenge)
	if err != nil {
		return err
	}
	if err := h.write(resp); err != nil {
		return err
	}
	return h.result()
}

// vncAuthResponse encrypts the challenge with DES. The key is the password,
// cut or padded to 8 bytes, with the bits of each byte in reverse order.
func vncAuthResponse(password string, challenge []byte) ([]byte, error) {
	key := make([]byte, 8)
	copy(key, password)
	for i, k := range key {
		key[i] = bits.Reverse8(k)
	}
	block, err := des.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(challenge))
	for i := 0; i+8 <= len(challenge); i += 8 {
		block.Encrypt(out[i:i+8], challenge[i:i+8])
	}
	return out, nil
}
