package mail

import "strings"

// AuthMessage turns a refused login into a message for the user. It reads
// the text that Gmail sends with the refusal.
func (m *Mailbox) AuthMessage(err error) string {
	if !strings.Contains(m.cfg.IMAPHost, "gmail") {
		return "The mail server refused the address or the app password. Make a new app password and update the connection."
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "application-specific password required"):
		return "Google needs an app password here, not the normal password of the account. Make an app password and paste it in the connection settings."
	case strings.Contains(s, "not enabled for imap"), strings.Contains(s, "imap access is disabled"), strings.Contains(s, "imap is disabled"):
		return "Your Google account does not let mail apps read this mailbox. If a company manages the account, ask the admin to let mail apps use it."
	case strings.Contains(s, "web browser"), strings.Contains(s, "webalert"):
		return "Google blocked the login. Open Gmail in a web browser on this computer and confirm that it was you. Then try again."
	default:
		return "Google refused the address or the app password. Google deletes all app passwords when you change the password of the account, and an admin can turn app passwords off. Make a new app password and update the connection."
	}
}
