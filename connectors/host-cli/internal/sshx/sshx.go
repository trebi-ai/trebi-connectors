// Package sshx runs commands on one server over SSH. It keeps one
// connection and opens it again after an error.
package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/config"
)

// dialTimeout bounds the TCP connect and the SSH handshake.
const dialTimeout = 15 * time.Second

// Errors that the adapter maps to a status.
var (
	// ErrAuth means that the server refused the key.
	ErrAuth = errors.New("the server refused the key")
	// ErrHostKey means that the server key is not the known key.
	ErrHostKey = errors.New("the server key is not the known key")
)

// Client runs commands on one server.
type Client struct {
	addr string
	cfg  *ssh.ClientConfig

	mu sync.Mutex
	c  *ssh.Client
}

// New builds a client from the settings. It does not connect.
func New(s config.Settings) (*Client, error) {
	var auth []ssh.AuthMethod
	if s.Key != nil {
		signer, err := ssh.ParsePrivateKey(s.Key)
		if err != nil {
			var pp *ssh.PassphraseMissingError
			if errors.As(err, &pp) {
				return nil, errors.New("the private key has a passphrase: use a key with no passphrase, or the SSH agent")
			}
			return nil, fmt.Errorf("private key: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if s.Agent != "" {
		sock := s.Agent
		auth = append(auth, ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			conn, err := net.DialTimeout("unix", sock, dialTimeout)
			if err != nil {
				return nil, fmt.Errorf("ssh agent: %w", err)
			}
			return agent.NewClient(conn).Signers()
		}))
	}
	cb, err := hostKeyCallback(s)
	if err != nil {
		return nil, err
	}
	return &Client{addr: s.Addr, cfg: &ssh.ClientConfig{User: s.User, Auth: auth, HostKeyCallback: cb, Timeout: dialTimeout}}, nil
}

// Run runs cmd and returns its stdout. A failed connection closes the
// client, so the next Run connects again.
func (c *Client) Run(ctx context.Context, cmd string) ([]byte, error) {
	cl, err := c.client(ctx)
	if err != nil {
		return nil, err
	}
	sess, err := cl.NewSession()
	if err != nil {
		c.drop(cl)
		return nil, err
	}
	defer sess.Close() //nolint:errcheck // the session ends with the command
	var stdout, stderr bytes.Buffer
	sess.Stdout, sess.Stderr = &stdout, &stderr
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case <-ctx.Done():
		c.drop(cl)
		return nil, ctx.Err()
	case err := <-done:
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			msg := fmt.Sprintf("the command exited with code %d", exit.ExitStatus())
			if e := strings.TrimSpace(stderr.String()); e != "" {
				msg += ": " + e
			}
			return stdout.Bytes(), errors.New(msg)
		}
		if err != nil {
			c.drop(cl)
			return nil, err
		}
		return stdout.Bytes(), nil
	}
}

// Close closes the connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.c == nil {
		return nil
	}
	err := c.c.Close()
	c.c = nil
	return err
}

func (c *Client) client(ctx context.Context) (*ssh.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.c != nil {
		return c.c, nil
	}
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, err
	}
	sc, chans, reqs, err := ssh.NewClientConn(conn, c.addr, c.cfg)
	if err != nil {
		conn.Close() //nolint:errcheck,gosec // the handshake error is the one to report
		switch {
		case errors.Is(err, ErrHostKey), errors.Is(err, errKeyMismatch):
			return nil, fmt.Errorf("%w: %v", ErrHostKey, err)
		case strings.Contains(err.Error(), "unable to authenticate"):
			return nil, fmt.Errorf("%w: %v", ErrAuth, err)
		}
		return nil, err
	}
	c.c = ssh.NewClient(sc, chans, reqs)
	return c.c, nil
}

func (c *Client) drop(cl *ssh.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.c == cl {
		c.c.Close() //nolint:errcheck,gosec // the connection is broken
		c.c = nil
	}
}

var errKeyMismatch = errors.New("the server key changed")

// hostKeyCallback checks the server key: against a pinned key, a
// known_hosts file, or the first key it sees (TOFU), which it then writes.
func hostKeyCallback(s config.Settings) (ssh.HostKeyCallback, error) {
	if s.HostKey != "" {
		want, err := parseHostKey(s.HostKey)
		if err != nil {
			return nil, err
		}
		fixed := ssh.FixedHostKey(want)
		return func(host string, remote net.Addr, key ssh.PublicKey) error {
			if err := fixed(host, remote, key); err != nil {
				return fmt.Errorf("%w for %s: the key is not the server key in the connection", errKeyMismatch, host)
			}
			return nil
		}, nil
	}
	if s.KnownHosts == "" {
		return nil, errors.New("no server key: set the server key")
	}
	return func(host string, remote net.Addr, key ssh.PublicKey) error {
		cb, err := knownhosts.New(s.KnownHosts)
		if errors.Is(err, os.ErrNotExist) {
			if !s.TOFU {
				return fmt.Errorf("%w: %s has no key for %s", ErrHostKey, s.KnownHosts, host)
			}
			return writeKnown(s.KnownHosts, host, key)
		}
		if err != nil {
			return err
		}
		err = cb(host, remote, key)
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) {
			if len(ke.Want) > 0 {
				return fmt.Errorf("%w for %s: the key is not the one that %s has", errKeyMismatch, host, filepath.Base(s.KnownHosts))
			}
			if s.TOFU {
				return writeKnown(s.KnownHosts, host, key)
			}
			return fmt.Errorf("%w: %s has no key for %s", ErrHostKey, s.KnownHosts, host)
		}
		return err
	}, nil
}

// parseHostKey reads "<type> <base64>" or a known_hosts line.
func parseHostKey(v string) (ssh.PublicKey, error) {
	if k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(v)); err == nil {
		return k, nil
	}
	_, _, k, _, _, err := ssh.ParseKnownHosts([]byte(v))
	if err != nil {
		return nil, fmt.Errorf("server key: %w", err)
	}
	return k, nil
}

func writeKnown(path, host string, key ssh.PublicKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(knownhosts.Line([]string{knownhosts.Normalize(host)}, key) + "\n"); err != nil {
		f.Close() //nolint:errcheck,gosec // the write error is the one to report
		return err
	}
	return f.Close()
}
