package vnctest

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"slices"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
)

// CA is a test certificate authority.
type CA struct {
	// PEM is the CA certificate, for a --tls-ca-cert file.
	PEM  []byte
	Pool *x509.CertPool
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// NewCA makes a CA that is valid for one day.
func NewCA() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "vnctest CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &CA{
		PEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		Pool: pool, cert: cert, key: key,
	}, nil
}

// ServerTLS is a server setting with a certificate for the hosts (names or
// IP addresses).
func (ca *CA) ServerTLS(hosts ...string) (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: hosts[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, nil
}

func (c *client) vncAuth() (bool, error) {
	challenge := make([]byte, 16)
	rand.Read(challenge)
	if err := c.write(challenge); err != nil {
		return false, err
	}
	resp, err := c.read(16)
	if err != nil {
		return false, err
	}
	return string(resp) == string(desResponse(c.s.opts.Password, challenge)), nil
}

// modp2048 is the RFC 3526 group 14 prime, which macOS also uses.
var modp2048, _ = new(big.Int).SetString(
	"FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B139B22514A08798E3404DD"+
		"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED"+
		"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE45B3DC2007CB8A163BF0598DA48361C55D39A69163FA8FD24CF5F"+
		"83655D23DCA3AD961C62F356208552BB9ED529077096966D670C354E4ABC9804F1746C08CA18217C32905E462E36CE3B"+
		"E39E772C180E86039B2783A2EC07A28FB5C55DF06F4C52C9DE2BCBF6955817183995497CEA956AE515D2261898FA0510"+
		"15728E5A8AACAA68FFFFFFFFFFFFFFFF", 16)

func (c *client) ard() (bool, error) {
	const keyLen = 256
	x, err := rand.Int(rand.Reader, modp2048)
	if err != nil {
		return false, err
	}
	pub := new(big.Int).Exp(big.NewInt(2), x, modp2048)
	msg := binary.BigEndian.AppendUint16(nil, 2)
	msg = binary.BigEndian.AppendUint16(msg, keyLen)
	msg = append(msg, modp2048.FillBytes(make([]byte, keyLen))...)
	msg = append(msg, pub.FillBytes(make([]byte, keyLen))...)
	if err := c.write(msg); err != nil {
		return false, err
	}
	resp, err := c.read(128 + keyLen)
	if err != nil {
		return false, err
	}
	clientPub := new(big.Int).SetBytes(resp[128:])
	shared := new(big.Int).Exp(clientPub, x, modp2048).FillBytes(make([]byte, keyLen))
	digest := md5.Sum(shared)
	block, _ := aes.NewCipher(digest[:])
	plain := make([]byte, 128)
	for i := 0; i < 128; i += aes.BlockSize {
		block.Decrypt(plain[i:], resp[i:])
	}
	field := func(b []byte) string {
		if i := bytes.IndexByte(b, 0); i >= 0 {
			return string(b[:i])
		}
		return ""
	}
	o := c.s.opts
	return field(plain[:64]) == o.User && field(plain[64:]) == o.Password, nil
}

func (c *client) veNCrypt() (bool, error) {
	o := c.s.opts
	subs := o.VeNCrypt
	if subs == nil {
		subs = []uint32{vnc.VeNCryptX509Vnc, vnc.VeNCryptX509Plain, vnc.VeNCryptX509None}
	}
	if err := c.write([]byte{0, 2}); err != nil {
		return false, err
	}
	v, err := c.read(2)
	if err != nil {
		return false, err
	}
	if v[0] != 0 || v[1] != 2 {
		c.write([]byte{1})
		return false, fmt.Errorf("client asked for VeNCrypt %d.%d", v[0], v[1])
	}
	msg := []byte{0, byte(len(subs))}
	for _, t := range subs {
		msg = binary.BigEndian.AppendUint32(msg, t)
	}
	if err := c.write(msg); err != nil {
		return false, err
	}
	b, err := c.read(4)
	if err != nil {
		return false, err
	}
	sub := binary.BigEndian.Uint32(b)
	if !slices.Contains(subs, sub) {
		return false, fmt.Errorf("client chose subtype %d, which was not offered", sub)
	}
	if o.TLS == nil {
		return false, errors.New("VeNCrypt needs Options.TLS")
	}
	if err := c.write([]byte{1}); err != nil {
		return false, err
	}
	tc := tls.Server(c.nc, o.TLS)
	if err := tc.Handshake(); err != nil {
		return false, err
	}
	c.nc, c.r = tc, bufio.NewReader(tc)
	switch sub {
	case vnc.VeNCryptX509Vnc, vnc.VeNCryptTLSVnc:
		return c.vncAuth()
	case vnc.VeNCryptX509Plain, vnc.VeNCryptTLSPlain, vnc.VeNCryptPlain:
		b, err := c.read(8)
		if err != nil {
			return false, err
		}
		user, err := c.read(int(binary.BigEndian.Uint32(b)))
		if err != nil {
			return false, err
		}
		pass, err := c.read(int(binary.BigEndian.Uint32(b[4:])))
		if err != nil {
			return false, err
		}
		return string(user) == o.User && string(pass) == o.Password, nil
	}
	return true, nil
}
