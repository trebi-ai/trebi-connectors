package cmd

import (
	"errors"
	"fmt"

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
			{
				Name:  "login",
				Usage: "Log in to Microsoft with a device code",
				Action: func(c *cli.Context) error {
					s := settingsFromCtx(c)
					if s.Trebi {
						return config.ErrSetInTrebi
					}
					if s.ClientID == "" {
						return errors.New("this build has no Microsoft app id; build it with MSTODO_CLIENT_ID set")
					}
					cl := client.New(s.ClientID, client.Session{})
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
					me, err := cl.Me(c.Context)
					if err != nil {
						return err
					}
					sess = cl.Session()
					sess.AccountID, sess.AccountName = me.ID, me.Name()
					if err := sess.Save(s.AuthPath()); err != nil {
						return err
					}
					_, err = fmt.Fprintf(c.App.Writer, "Logged in as %s\n", me.Name())
					return err
				},
			},
		},
	}
}
