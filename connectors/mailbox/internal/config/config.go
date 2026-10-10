// Package config finds the account settings. It is the only code that
// reads the MAILBOX_* env names. In Trebi mode (TREBI_STATE_DIR is set) the
// values come only from the env that Trebi gives.
package config

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/trebi-ai/trebi-connectors/sdk"
)

// Env names of the settings.
const (
	EnvAddress  = "MAILBOX_ADDRESS"
	EnvPassword = "MAILBOX_PASSWORD"
	EnvProvider = "MAILBOX_PROVIDER"
	EnvIMAPHost = "MAILBOX_IMAP_HOST"
	EnvSMTPHost = "MAILBOX_SMTP_HOST"
	EnvDrafts   = "MAILBOX_DRAFTS"
	EnvSent     = "MAILBOX_SENT"
)

// Labels of the inputs in the catalog manifest.
const (
	AddressLabel  = "Email address"
	PasswordLabel = "App password"
)

// Provider is a preset of hosts and folder names.
type Provider struct {
	IMAPHost string
	SMTPHost string
	Drafts   string
	// Sent is the folder for the sent copy. "" means that the server keeps
	// the copy itself.
	Sent string
}

// Providers are the known presets.
var Providers = map[string]Provider{
	"gmail":    {IMAPHost: "imap.gmail.com:993", SMTPHost: "smtp.gmail.com:587", Drafts: "[Gmail]/Drafts"},
	"icloud":   {IMAPHost: "imap.mail.me.com:993", SMTPHost: "smtp.mail.me.com:587", Drafts: "Drafts", Sent: "Sent Messages"},
	"fastmail": {IMAPHost: "imap.fastmail.com:993", SMTPHost: "smtp.fastmail.com:587", Drafts: "Drafts", Sent: "Sent"},
	"outlook":  {IMAPHost: "outlook.office365.com:993", SMTPHost: "smtp.office365.com:587", Drafts: "Drafts", Sent: "Sent Items"},
}

// Settings is one account.
type Settings struct {
	Address  string
	Password string
	IMAPHost string // host:port; port 993 or other is TLS
	SMTPHost string // host:port; port 465 is TLS, other ports use STARTTLS
	Drafts   string // "" finds the \Drafts folder
	Sent     string // "" keeps no sent copy
	Trebi    bool
}

// Resolve reads the settings from the env. provider is the --provider
// flag; it wins over MAILBOX_PROVIDER. An explicit host or folder env wins
// over the preset.
func Resolve(provider string) (Settings, error) {
	_, trebi := sdk.FromEnv()
	if provider == "" {
		provider = os.Getenv(EnvProvider)
	}
	s := Settings{Trebi: trebi}
	if provider != "" {
		p, ok := Providers[strings.ToLower(provider)]
		if !ok {
			return s, fmt.Errorf("unknown provider %q: use gmail, icloud, fastmail, or outlook", provider)
		}
		s.IMAPHost, s.SMTPHost, s.Drafts, s.Sent = p.IMAPHost, p.SMTPHost, p.Drafts, p.Sent
	} else {
		s.Sent = "Sent"
	}
	s.Address = strings.TrimSpace(os.Getenv(EnvAddress))
	s.Password = os.Getenv(EnvPassword)
	for env, dst := range map[string]*string{EnvIMAPHost: &s.IMAPHost, EnvSMTPHost: &s.SMTPHost, EnvDrafts: &s.Drafts, EnvSent: &s.Sent} {
		if v, ok := os.LookupEnv(env); ok {
			*dst = strings.TrimSpace(v)
		}
	}
	s.IMAPHost = withPort(s.IMAPHost, "993")
	s.SMTPHost = withPort(s.SMTPHost, "587")
	return s, nil
}

// Missing returns the first required setting that is not set, or nil.
func (s Settings) Missing() *sdk.MissingInput {
	switch {
	case s.Address == "":
		return &sdk.MissingInput{Name: EnvAddress, Label: AddressLabel}
	case s.Password == "":
		return &sdk.MissingInput{Name: EnvPassword, Label: PasswordLabel}
	case s.IMAPHost == "":
		return &sdk.MissingInput{Name: EnvIMAPHost, Label: "IMAP server"}
	case s.SMTPHost == "":
		return &sdk.MissingInput{Name: EnvSMTPHost, Label: "SMTP server"}
	}
	return nil
}

func withPort(host, port string) string {
	if host == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(host, port)
}
