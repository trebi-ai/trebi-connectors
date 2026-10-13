// Package fakessh is an SSH server in the same process that answers each
// command with fixed output. The tests and `serve --sandbox` use it.
package fakessh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"net"
	"sync"

	"golang.org/x/crypto/ssh"
)

// User is the only user that the server accepts.
const User = "sandbox"

// Reply gives the stdout and the exit code of the n-th command (from 1).
type Reply func(n int, cmd string) (stdout string, code int)

// Server is a running fake server.
type Server struct {
	ln      net.Listener
	hostKey ssh.Signer
	client  ssh.Signer
	keyPEM  []byte

	mu      sync.Mutex
	reply   Reply
	n       int
	refuse  bool
	conns   []net.Conn
	closing bool
}

// Start runs a server on 127.0.0.1 with a new host key and a new client
// key. It accepts only the client key and the user User.
func Start(reply Reply) (*Server, error) {
	_, hk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	hostKey, err := ssh.NewSignerFromKey(hk)
	if err != nil {
		return nil, err
	}
	_, ck, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	client, err := ssh.NewSignerFromKey(ck)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(ck, "")
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, hostKey: hostKey, client: client, keyPEM: pem.EncodeToMemory(block), reply: reply}
	go s.accept()
	return s, nil
}

// Addr is host:port of the server.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// ClientKey is the PEM private key that the server accepts.
func (s *Server) ClientKey() []byte { return s.keyPEM }

// ClientSigner is the key that the server accepts, for an agent.
func (s *Server) ClientSigner() ssh.Signer { return s.client }

// HostKey is the server key in authorized_keys form.
func (s *Server) HostKey() string {
	return string(ssh.MarshalAuthorizedKey(s.hostKey.PublicKey()))
}

// SetReply replaces the reply function.
func (s *Server) SetReply(r Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reply = r
}

// Refuse makes the server refuse each key.
func (s *Server) Refuse(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse = v
}

// Commands returns the count of commands so far.
func (s *Server) Commands() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// Close stops the server and closes each connection.
func (s *Server) Close() {
	s.mu.Lock()
	s.closing = true
	conns := s.conns
	s.mu.Unlock()
	s.ln.Close() //nolint:errcheck,gosec // test server
	for _, c := range conns {
		c.Close() //nolint:errcheck,gosec // test server
	}
}

func (s *Server) config() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			s.mu.Lock()
			refuse := s.refuse
			s.mu.Unlock()
			if refuse || meta.User() != User || string(key.Marshal()) != string(s.client.PublicKey().Marshal()) {
				return nil, fmt.Errorf("refused")
			}
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(s.hostKey)
	return cfg
}

func (s *Server) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			c.Close() //nolint:errcheck,gosec // test server
			return
		}
		s.conns = append(s.conns, c)
		s.mu.Unlock()
		go s.serve(c)
	}
}

func (s *Server) serve(c net.Conn) {
	_, chans, reqs, err := ssh.NewServerConn(c, s.config())
	if err != nil {
		c.Close() //nolint:errcheck,gosec // test server
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "only sessions") //nolint:errcheck,gosec // test server
			continue
		}
		ch, in, err := nc.Accept()
		if err != nil {
			continue
		}
		go s.session(ch, in)
	}
}

func (s *Server) session(ch ssh.Channel, in <-chan *ssh.Request) {
	defer ch.Close() //nolint:errcheck // test server
	for req := range in {
		if req.Type != "exec" {
			req.Reply(false, nil) //nolint:errcheck,gosec // test server
			continue
		}
		var p struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &p); err != nil {
			req.Reply(false, nil) //nolint:errcheck,gosec // test server
			return
		}
		req.Reply(true, nil) //nolint:errcheck,gosec // test server
		s.mu.Lock()
		s.n++
		n, reply := s.n, s.reply
		s.mu.Unlock()
		out, code := reply(n, p.Command)
		ch.Write([]byte(out)) //nolint:errcheck,gosec // test server
		status := make([]byte, 4)
		binary.BigEndian.PutUint32(status, uint32(code)) //nolint:gosec // exit codes are small
		ch.SendRequest("exit-status", false, status)     //nolint:errcheck,gosec // test server
		return
	}
}
