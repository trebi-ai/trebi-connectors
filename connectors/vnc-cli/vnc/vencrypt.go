package vnc

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
)

// VeNCrypt subtypes (rfbproto).
const (
	VeNCryptPlain     uint32 = 256
	VeNCryptTLSNone   uint32 = 257
	VeNCryptTLSVnc    uint32 = 258
	VeNCryptTLSPlain  uint32 = 259
	VeNCryptX509None  uint32 = 260
	VeNCryptX509Vnc   uint32 = 261
	VeNCryptX509Plain uint32 = 262
)

var veNCryptNames = map[uint32]string{
	256: "Plain", 257: "TLSNone", 258: "TLSVnc", 259: "TLSPlain",
	260: "X509None", 261: "X509Vnc", 262: "X509Plain", 263: "TLSSASL", 264: "X509SASL",
}

func veNCryptName(t uint32) string {
	if n, ok := veNCryptNames[t]; ok {
		return fmt.Sprintf("%s (%d)", n, t)
	}
	if t < 256 {
		return securityName(uint8(t))
	}
	return fmt.Sprintf("subtype %d", t)
}

// veNCryptOrder is the subtype preference, the most protected first (as
// vncdotool). Bare Plain is never used: it sends the password with no
// encryption.
var veNCryptOrder = []uint32{
	VeNCryptX509Vnc, VeNCryptX509Plain, VeNCryptX509None,
	VeNCryptTLSVnc, VeNCryptTLSPlain, VeNCryptTLSNone,
}

// errVeNCryptUnusable is a VeNCrypt list with no subtype that this client
// can use. Dial then tries again without VeNCrypt.
var errVeNCryptUnusable = errors.New("no usable VeNCrypt subtype")

// NewTLSConfig is the TLS setting for VeNCrypt. caFile is a PEM file of CA
// certificates that replace the system roots; "" keeps the system roots.
// insecure skips the certificate check.
func NewTLSConfig(caFile string, insecure bool) (*tls.Config, error) {
	cfg := &tls.Config{InsecureSkipVerify: insecure}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read the CA certificate: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no PEM certificate in %s", caFile)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// secVeNCrypt is VeNCrypt (type 19), version 0.2.
type secVeNCrypt struct{}

func (secVeNCrypt) handshake(h *handshake) error {
	v, err := h.read(2)
	if err != nil {
		return err
	}
	if v[0] == 0 && v[1] < 2 {
		return protoErr("VeNCrypt", "version %d.%d is not supported", v[0], v[1])
	}
	if err := h.write([]byte{0, 2}); err != nil {
		return err
	}
	ack, err := h.read(1)
	if err != nil {
		return err
	}
	if ack[0] != 0 {
		return protoErr("VeNCrypt", "the server refused version 0.2")
	}
	n, err := h.read(1)
	if err != nil {
		return err
	}
	if n[0] == 0 {
		return protoErr("VeNCrypt", "the server offers no subtype")
	}
	b, err := h.read(4 * int(n[0]))
	if err != nil {
		return err
	}
	offered := make([]uint32, n[0])
	for i := range offered {
		offered[i] = binary.BigEndian.Uint32(b[i*4:])
	}
	sub, err := h.chooseVeNCrypt(offered)
	if err != nil {
		return err
	}
	if err := h.write(binary.BigEndian.AppendUint32(nil, sub)); err != nil {
		return err
	}
	if ack, err = h.read(1); err != nil {
		return err
	}
	if ack[0] != 1 {
		return protoErr("VeNCrypt", "the server refused %s", veNCryptName(sub))
	}
	if h.r.Buffered() > 0 {
		return protoErr("VeNCrypt", "the server sent data before the TLS handshake")
	}
	if err := h.startTLS(); err != nil {
		return err
	}
	switch sub {
	case VeNCryptX509Vnc, VeNCryptTLSVnc:
		return secVNC{}.handshake(h)
	case VeNCryptX509Plain, VeNCryptTLSPlain:
		msg := binary.BigEndian.AppendUint32(nil, uint32(len(h.opts.User)))
		msg = binary.BigEndian.AppendUint32(msg, uint32(len(h.opts.Password)))
		msg = append(append(msg, h.opts.User...), h.opts.Password...)
		if err := h.write(msg); err != nil {
			return err
		}
	}
	// A VeNCrypt subtype always has a SecurityResult, also before 3.8.
	return h.result()
}

// veNCryptUnusable says why the client does not use the subtype, or "".
func (h *handshake) veNCryptUnusable(t uint32) string {
	switch t {
	case VeNCryptPlain:
		return "sends the password with no encryption"
	case VeNCryptTLSNone, VeNCryptTLSVnc, VeNCryptTLSPlain:
		return "anonymous TLS is not supported"
	case VeNCryptX509None:
	case VeNCryptX509Vnc:
		if h.opts.Password == "" {
			return "no password is set"
		}
	case VeNCryptX509Plain:
		if h.opts.User == "" || h.opts.Password == "" {
			return "no user and password are set"
		}
	default:
		return "not supported"
	}
	if h.tlsConfig().ServerName == "" && !h.tlsConfig().InsecureSkipVerify {
		return "no server name to check the certificate against"
	}
	return ""
}

func (h *handshake) chooseVeNCrypt(offered []uint32) (uint32, error) {
	for _, t := range veNCryptOrder {
		for _, o := range offered {
			if o == t && h.veNCryptUnusable(t) == "" {
				return t, nil
			}
		}
	}
	reasons := make([]string, len(offered))
	for i, t := range offered {
		reasons[i] = veNCryptName(t) + ": " + h.veNCryptUnusable(t)
	}
	return 0, &ProtocolError{Step: "VeNCrypt", Err: fmt.Errorf("%w; the server offers %s", errVeNCryptUnusable, strings.Join(reasons, "; "))}
}

// tlsConfig is Options.TLS with the server name from the address.
func (h *handshake) tlsConfig() *tls.Config {
	cfg := &tls.Config{}
	if h.opts.TLS != nil {
		cfg = h.opts.TLS.Clone()
	}
	if cfg.ServerName == "" {
		cfg.ServerName = h.host
	}
	if cfg.MinVersion == 0 {
		cfg.MinVersion = tls.VersionTLS12
	}
	return cfg
}

// startTLS puts TLS over the connection. The handshake reads and writes
// through it from now on.
func (h *handshake) startTLS() error {
	cfg := h.tlsConfig()
	tc := tls.Client(h.conn, cfg)
	if err := tc.Handshake(); err != nil {
		var host x509.HostnameError
		var verify *tls.CertificateVerificationError
		switch {
		case errors.As(err, &host):
			return protoErr("TLS", "the certificate does not match the server name %q: %w", cfg.ServerName, err)
		case errors.As(err, &verify):
			return protoErr("TLS", "the certificate of %q is not trusted: %w", cfg.ServerName, err)
		}
		return protoErr("TLS", "handshake failed: %w", err)
	}
	h.conn = tc
	h.r = bufio.NewReaderSize(tc, h.r.Size())
	return nil
}
