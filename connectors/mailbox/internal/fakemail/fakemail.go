// Package fakemail is an IMAP and SMTP server in memory for one account.
// The tests and `serve --sandbox` use it. Both servers use TLS with a
// certificate that only ClientTLS trusts.
package fakemail

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// Folders that the fake makes.
var Folders = []string{"INBOX", "Drafts", "Sent"}

// Mail is one message that the SMTP server got.
type Mail struct {
	From string
	To   []string
	Data []byte
}

// Server is one fake account.
type Server struct {
	user     *imapmemserver.User
	imap     *imapserver.Server
	imapLn   net.Listener
	smtpLn   net.Listener
	cert     tls.Certificate
	pool     *x509.CertPool
	address  string
	password string

	mu   sync.Mutex
	sent []Mail
	wg   sync.WaitGroup
}

// Start starts both servers on 127.0.0.1 for one address and password.
func Start(address, password string) (*Server, error) {
	cert, pool, err := selfSigned()
	if err != nil {
		return nil, err
	}
	s := &Server{cert: cert, pool: pool, address: address, password: password}
	s.user = imapmemserver.NewUser(address, password)
	for _, f := range Folders {
		if err := s.user.Create(f, nil); err != nil {
			return nil, err
		}
	}
	mem := imapmemserver.New()
	mem.AddUser(s.user)
	s.imap = imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		Logger:       quiet{},
		InsecureAuth: true,
	})
	srvTLS := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	if s.imapLn, err = tls.Listen("tcp", "127.0.0.1:0", srvTLS); err != nil {
		return nil, err
	}
	if s.smtpLn, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
		_ = s.imapLn.Close()
		return nil, err
	}
	go func() { _ = s.imap.Serve(s.imapLn) }() // ends on Close
	s.wg.Add(1)
	go s.acceptSMTP(srvTLS)
	return s, nil
}

// IMAPAddr is host:port of the IMAP server (TLS).
func (s *Server) IMAPAddr() string { return s.imapLn.Addr().String() }

// SMTPAddr is host:port of the SMTP server (STARTTLS).
func (s *Server) SMTPAddr() string { return s.smtpLn.Addr().String() }

// ClientTLS trusts the certificate of the fake.
func (s *Server) ClientTLS() *tls.Config {
	return &tls.Config{RootCAs: s.pool, MinVersion: tls.VersionTLS12}
}

// Deliver puts a message in a folder.
func (s *Server) Deliver(folder string, raw []byte, flags ...imap.Flag) error {
	_, err := s.user.Append(folder, literal{bytes.NewReader(raw), int64(len(raw))}, &imap.AppendOptions{Flags: flags, Time: time.Now()})
	return err
}

// Sent returns the messages that the SMTP server got.
func (s *Server) Sent() []Mail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Mail(nil), s.sent...)
}

// Close stops both servers.
func (s *Server) Close() {
	_ = s.imap.Close()
	_ = s.smtpLn.Close()
	s.wg.Wait()
}

type literal struct {
	*bytes.Reader
	n int64
}

func (l literal) Size() int64 { return l.n }

type quiet struct{}

func (quiet) Printf(string, ...any) {}

func (s *Server) acceptSMTP(cfg *tls.Config) {
	defer s.wg.Done()
	for {
		c, err := s.smtpLn.Accept()
		if err != nil {
			return
		}
		go s.smtpSession(c, cfg)
	}
}

// smtpSession serves EHLO, STARTTLS, AUTH PLAIN, MAIL, RCPT, DATA, RSET,
// NOOP, and QUIT. AUTH needs TLS.
func (s *Server) smtpSession(c net.Conn, cfg *tls.Config) {
	defer c.Close() //nolint:errcheck // the session is over
	_ = c.SetDeadline(time.Now().Add(time.Minute))
	r, w := bufio.NewReader(c), bufio.NewWriter(c)
	say := func(lines ...string) {
		for _, l := range lines {
			_, _ = w.WriteString(l + "\r\n")
		}
		_ = w.Flush()
	}
	say("220 fakemail ESMTP")
	secure, authed := false, false
	var cur Mail
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			if secure {
				say("250-fakemail", "250 AUTH PLAIN")
			} else {
				say("250-fakemail", "250 STARTTLS")
			}
		case "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(c, cfg)
			if tc.Handshake() != nil {
				return
			}
			c, secure = tc, true
			r, w = bufio.NewReader(c), bufio.NewWriter(c)
		case "AUTH":
			mech, resp, _ := strings.Cut(arg, " ")
			if !secure || !strings.EqualFold(mech, "PLAIN") {
				say("504 use AUTH PLAIN after STARTTLS")
				continue
			}
			b, err := base64.StdEncoding.DecodeString(resp)
			parts := strings.Split(string(b), "\x00")
			if err != nil || len(parts) != 3 || parts[1] != s.address || parts[2] != s.password {
				say("535 5.7.8 bad credentials")
				continue
			}
			authed = true
			say("235 ok")
		case "MAIL":
			if !authed {
				say("530 log in first")
				continue
			}
			cur = Mail{From: addrArg(arg)}
			say("250 ok")
		case "RCPT":
			cur.To = append(cur.To, addrArg(arg))
			say("250 ok")
		case "DATA":
			say("354 send it")
			var data bytes.Buffer
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				data.WriteString(strings.TrimPrefix(l, "."))
			}
			cur.Data = data.Bytes()
			s.mu.Lock()
			s.sent = append(s.sent, cur)
			s.mu.Unlock()
			say("250 queued")
		case "RSET", "NOOP":
			say("250 ok")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("502 not here")
		}
	}
}

func addrArg(arg string) string {
	_, v, _ := strings.Cut(arg, ":")
	return strings.Trim(strings.TrimSpace(v), "<>")
}

func selfSigned() (tls.Certificate, *x509.CertPool, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "fakemail"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,

		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("fake certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool, nil
}
