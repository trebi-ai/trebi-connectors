package cmd

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/config"
)

// AuthCommand returns the `auth` command.
func AuthCommand() *cli.Command {
	return &cli.Command{
		Name:  "auth",
		Usage: "Manage the Microsoft login",
		Subcommands: []*cli.Command{
			leaf(&cli.Command{
				Name:   "login",
				Usage:  "Log in to Microsoft with a device code",
				Action: authLogin,
			}),
			leaf(&cli.Command{
				Name:   "status",
				Usage:  "Show the account and check the token",
				Action: authStatus,
			}),
			leaf(&cli.Command{
				Name:   "logout",
				Usage:  "Forget the login",
				Action: authLogout,
			}),
		},
	}
}

func authLogin(c *cli.Context) error {
	s := settingsFromCtx(c)
	if s.Trebi {
		return config.ErrSetInTrebi
	}
	if s.ClientID == "" {
		return fmt.Errorf("no Microsoft app id: set %s or pass --client-id", config.EnvClientID)
	}
	cl := newClient(c, client.Session{})
	dc, err := cl.BeginDeviceCode(c.Context)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.App.ErrWriter, "Open %s and type the code %s\n", dc.VerificationURI, dc.UserCode); err != nil {
		return err
	}
	sess, err := cl.WaitDeviceCode(c.Context, dc)
	if err != nil {
		return err
	}
	cl.SetSession(sess)
	if err := cl.Ping(c.Context); err != nil {
		return fmt.Errorf("the login has no access to Microsoft To Do: %w", err)
	}
	if err := sess.Save(s.AuthPath()); err != nil {
		return err
	}
	if c.Bool("json") {
		return printJSON(c.App.Writer, map[string]string{"account_id": sess.AccountID, "account": sess.AccountName})
	}
	_, err = fmt.Fprintf(c.App.Writer, "Logged in as %s\n", sess.AccountName)
	return err
}

type authInfo struct {
	LoggedIn  bool      `json:"logged_in"`
	Valid     bool      `json:"valid"`
	Account   string    `json:"account,omitempty"`
	AccountID string    `json:"account_id,omitempty"`
	ClientID  string    `json:"client_id,omitempty"`
	Tenant    string    `json:"tenant,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	File      string    `json:"file"`
	Error     string    `json:"error,omitempty"`
}

func authStatus(c *cli.Context) error {
	s := settingsFromCtx(c)
	sess, err := client.LoadSession(s.AuthPath())
	if err != nil {
		return err
	}
	info := authInfo{
		LoggedIn: sess.AccessToken != "", Account: sess.AccountName, AccountID: sess.AccountID,
		ClientID: sess.ClientID, Tenant: sess.Tenant, ExpiresAt: sess.ExpiresAt, File: s.AuthPath(),
	}
	if info.LoggedIn {
		cl, err := clientFromCtx(c)
		if err == nil {
			err = cl.Ping(c.Context)
		}
		info.Valid = err == nil
		if err != nil {
			info.Error = err.Error()
		}
	}
	if c.Bool("json") {
		return printJSON(c.App.Writer, info)
	}
	switch {
	case !info.LoggedIn:
		_, err = fmt.Fprintln(c.App.Writer, "Not logged in.")
	case info.Valid:
		_, err = fmt.Fprintf(c.App.Writer, "Logged in as %s. The token works.\n", info.Account)
	default:
		_, err = fmt.Fprintf(c.App.Writer, "Logged in as %s. The token does not work: %s\n", info.Account, info.Error)
	}
	return err
}

func authLogout(c *cli.Context) error {
	s := settingsFromCtx(c)
	if s.Trebi {
		return config.ErrSetInTrebi
	}
	if err := os.Remove(s.AuthPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if c.Bool("json") {
		return printJSON(c.App.Writer, map[string]bool{"logged_out": true})
	}
	_, err := fmt.Fprintln(c.App.Writer, "Logged out.")
	return err
}
