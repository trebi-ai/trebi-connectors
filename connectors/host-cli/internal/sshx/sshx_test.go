package sshx_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/fakessh"
	"github.com/trebi-ai/trebi-connectors/connectors/host-cli/internal/sshx"
)

func start(t *testing.T) *fakessh.Server {
	t.Helper()
	fake, err := fakessh.Start(func(n int, cmd string) (string, int) {
		if cmd == "fail" {
			return "", 3
		}
		return "out " + cmd, 0
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	return fake
}

func run(t *testing.T, s config.Settings, cmd string) (string, error) {
	t.Helper()
	cl, err := sshx.New(s)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close() //nolint:errcheck // test
	out, err := cl.Run(context.Background(), cmd)
	return string(out), err
}

func TestPinnedKey(t *testing.T) {
	fake := start(t)
	s := config.Settings{Addr: fake.Addr(), User: fakessh.User, Key: fake.ClientKey(), HostKey: fake.HostKey()}
	if out, err := run(t, s, "uptime"); err != nil || out != "out uptime" {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := run(t, s, "fail"); err == nil || !strings.Contains(err.Error(), "code 3") {
		t.Fatalf("got %v", err)
	}
	other := start(t)
	s.HostKey = other.HostKey()
	if _, err := run(t, s, "uptime"); !errors.Is(err, sshx.ErrHostKey) {
		t.Fatalf("got %v", err)
	}
}

func TestTrustOnFirstUse(t *testing.T) {
	fake := start(t)
	known := filepath.Join(t.TempDir(), config.KnownHostsFile)
	s := config.Settings{Addr: fake.Addr(), User: fakessh.User, Key: fake.ClientKey(), KnownHosts: known, TOFU: true}
	if _, err := run(t, s, "a"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(known)
	if err != nil || !strings.Contains(string(b), "ssh-ed25519") {
		t.Fatalf("%s %v", b, err)
	}
	if _, err := run(t, s, "b"); err != nil {
		t.Fatalf("the second connect must pass: %v", err)
	}
	// A second server shows another key for an address in the file.
	other := start(t)
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(fake.HostKey()))
	if err != nil {
		t.Fatal(err)
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(other.Addr())}, key) + "\n"
	if err := os.WriteFile(known, append(b, line...), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Addr, s.Key = other.Addr(), other.ClientKey()
	if _, err := run(t, s, "c"); !errors.Is(err, sshx.ErrHostKey) {
		t.Fatalf("got %v", err)
	}
	s.TOFU = false
	s.Addr, s.KnownHosts = fake.Addr(), filepath.Join(t.TempDir(), "none")
	if _, err := run(t, s, "d"); !errors.Is(err, sshx.ErrHostKey) {
		t.Fatalf("no file and no TOFU: got %v", err)
	}
}

func TestRefusedKey(t *testing.T) {
	fake := start(t)
	fake.Refuse(true)
	s := config.Settings{Addr: fake.Addr(), User: fakessh.User, Key: fake.ClientKey(), HostKey: fake.HostKey()}
	if _, err := run(t, s, "a"); !errors.Is(err, sshx.ErrAuth) {
		t.Fatalf("got %v", err)
	}
}

func TestAgent(t *testing.T) {
	fake := start(t)
	raw, err := ssh.ParseRawPrivateKey(fake.ClientKey())
	if err != nil {
		t.Fatal(err)
	}
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: raw}); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "ag")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) }) //nolint:errcheck,gosec // test folder
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() }) //nolint:errcheck,gosec // test listener
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go agent.ServeAgent(keyring, c) //nolint:errcheck // test agent
		}
	}()
	s := config.Settings{Addr: fake.Addr(), User: fakessh.User, Agent: sock, HostKey: fake.HostKey()}
	if out, err := run(t, s, "x"); err != nil || out != "out x" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestPassphraseKey(t *testing.T) {
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(k, "", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	fake := start(t)
	_, err = sshx.New(config.Settings{Addr: "h:22", User: "u", Key: pem.EncodeToMemory(block), HostKey: fake.HostKey()})
	if err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("got %v", err)
	}
}
