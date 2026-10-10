package config_test

import (
	"testing"

	"github.com/trebi-ai/trebi-connectors/connectors/mailbox/internal/config"
)

func TestResolveProvider(t *testing.T) {
	t.Setenv("TREBI_STATE_DIR", t.TempDir())
	t.Setenv(config.EnvAddress, "me@gmail.com")
	t.Setenv(config.EnvPassword, "pw")
	s, err := config.Resolve("gmail")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Trebi || s.IMAPHost != "imap.gmail.com:993" || s.SMTPHost != "smtp.gmail.com:587" || s.Drafts != "[Gmail]/Drafts" || s.Sent != "" {
		t.Fatalf("%+v", s)
	}
	if s.Missing() != nil {
		t.Fatalf("missing %v", s.Missing())
	}
	t.Setenv(config.EnvDrafts, "Entwürfe")
	t.Setenv(config.EnvSMTPHost, "smtp.example.com")
	s, _ = config.Resolve("gmail")
	if s.Drafts != "Entwürfe" || s.SMTPHost != "smtp.example.com:587" {
		t.Fatalf("an env must win over the preset: %+v", s)
	}
	if _, err := config.Resolve("aol"); err == nil {
		t.Fatal("want an error for an unknown provider")
	}
}

func TestResolveStandalone(t *testing.T) {
	t.Setenv("TREBI_STATE_DIR", "")
	t.Setenv(config.EnvAddress, "")
	t.Setenv(config.EnvPassword, "")
	t.Setenv(config.EnvProvider, "fastmail")
	s, err := config.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if s.Trebi || s.IMAPHost != "imap.fastmail.com:993" || s.Sent != "Sent" {
		t.Fatalf("%+v", s)
	}
	if m := s.Missing(); m == nil || m.Name != config.EnvAddress {
		t.Fatalf("missing %v", m)
	}
	t.Setenv(config.EnvProvider, "")
	t.Setenv(config.EnvAddress, "me@example.com")
	t.Setenv(config.EnvPassword, "pw")
	s, _ = config.Resolve("")
	if m := s.Missing(); m == nil || m.Name != config.EnvIMAPHost {
		t.Fatalf("missing %v", m)
	}
}
