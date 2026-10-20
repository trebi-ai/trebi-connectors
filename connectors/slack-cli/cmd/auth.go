package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	cli "github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/localauth"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/output"
)

// AuthCommand returns the `auth` command tree.
func AuthCommand() *cli.Command {
	return &cli.Command{
		Name:  "auth",
		Usage: "Manage Slack workspace credentials",
		Description: `slack-cli keeps one or more workspaces in ~/.cli-tools/slack-cli/config.toml (mode 0600).
With TREBI_STATE_DIR set, the file is in that folder, Trebi sets the
workspaces, and import, slk, add, use, and remove fail.

Get credentials in one of these ways:
  auth import   Read the signed-in workspaces from the Slack desktop app.
  auth slk      Import tokens saved by gammons/slk.
  auth add      Paste a token: xoxp-/xoxb- (OAuth) or xoxc- with --cookie xoxd-... (session).

Pick a default with "auth use <name>". Override per call with --workspace/-w.
A -w value matches a workspace name, team id, domain, or team name.`,
		Commands: []*cli.Command{
			savesSetting(authImportCommand()),
			savesSetting(authSlkCommand()),
			savesSetting(authAddCommand()),
			authListCommand(),
			savesSetting(authUseCommand()),
			savesSetting(authRemoveCommand()),
			authTestCommand(),
			authShowCommand(),
		},
	}
}

// savesSetting makes a command that writes the config fail in Trebi mode:
// the workspaces come from the connection.
func savesSetting(c *cli.Command) *cli.Command {
	action := c.Action
	c.Action = func(ctx context.Context, c *cli.Command) error {
		if config.Trebi() {
			return config.ErrSetInTrebi
		}
		return action(ctx, c)
	}
	return c
}

// mintToken gets a fresh xoxc- token from a workspace web page with its cookie.
func mintToken(ctx context.Context, hc *http.Client, w config.Workspace) (string, error) {
	base := w.URL
	if base == "" && w.Domain != "" {
		base = "https://" + w.Domain + ".slack.com"
	}
	if base == "" {
		return "", errors.New("no workspace URL to mint a token from")
	}
	return localauth.Mint(ctx, hc, strings.TrimRight(base, "/"), w.Cookie)
}

func sessionFromImport(ctx context.Context, s localauth.Session, verify bool) (config.Workspace, error) {
	w := config.Workspace{
		Token: s.Token, Cookie: s.Cookie, TeamID: s.TeamID, TeamName: s.TeamName,
		Domain: s.Domain, URL: s.URL, Source: config.SourceDesktop,
	}
	if w.URL == "" && w.Domain != "" {
		w.URL = "https://" + w.Domain + ".slack.com"
	}
	if verify {
		id, err := verifyWorkspace(ctx, &w)
		if err != nil {
			return w, err
		}
		_ = id
	}
	return w, nil
}

// verifyWorkspace calls auth.test and fills the identity fields.
func verifyWorkspace(ctx context.Context, w *config.Workspace) (authTest, error) {
	cl := client.New(*w)
	var at authTest
	if err := cl.CallJSON(ctx, "auth.test", url.Values{}, &at); err != nil {
		return at, err
	}
	w.TeamID, w.TeamName, w.URL, w.UserID, w.User = at.TeamID, at.Team, at.URL, at.UserID, at.User
	if w.Domain == "" {
		w.Domain = domainFromURL(at.URL)
	}
	return at, nil
}

type authTest struct {
	URL    string `json:"url"`
	Team   string `json:"team"`
	User   string `json:"user"`
	TeamID string `json:"team_id"`
	UserID string `json:"user_id"`
	BotID  string `json:"bot_id"`
}

func domainFromURL(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(p.Host, ".slack.com")
}

func authImportCommand() *cli.Command {
	return &cli.Command{
		Name:  "import",
		Usage: "Import workspaces from the Slack desktop app",
		Description: `Reads the signed-in workspaces from the Slack desktop app: the session
token (xoxc-) from Local Storage and the "d" cookie from the Cookies DB,
decrypted with the OS keyring. Each workspace is saved by its domain.

macOS may prompt once to let slack-cli read the "Slack Safe Storage" key.`,
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "no-verify", Usage: "Skip the auth.test check"},
			&cli.BoolFlag{Name: "set-default", Usage: "Make the first imported workspace the default"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			d, err := localauth.NewDesktop()
			if err != nil {
				return err
			}
			hc := &http.Client{}
			sessions, err := d.Sessions(func(domain, cookie string) (string, error) {
				return localauth.Mint(ctx, hc, "https://"+domain+".slack.com", cookie)
			})
			if err != nil {
				return err
			}
			return importSessions(ctx, a, c, sessions, !c.Bool("no-verify"), c.Bool("set-default"))
		},
	}
}

func authSlkCommand() *cli.Command {
	return &cli.Command{
		Name:  "slk",
		Usage: "Import tokens saved by gammons/slk",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "dir", Usage: "slk token dir (default ~/.local/share/slk/tokens)"},
			&cli.BoolFlag{Name: "no-verify", Usage: "Skip the auth.test check"},
			&cli.BoolFlag{Name: "set-default", Usage: "Make the first imported workspace the default"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			dir := c.String("dir")
			if dir == "" {
				dir = localauth.SlkDir()
			}
			sessions, err := localauth.SlkSessions(dir)
			if err != nil {
				return err
			}
			if len(sessions) == 0 {
				return fmt.Errorf("no slk tokens in %s", dir)
			}
			return importSessions(ctx, a, c, sessions, !c.Bool("no-verify"), c.Bool("set-default"))
		},
	}
}

func importSessions(ctx context.Context, a *AppContext, c *cli.Command, sessions []localauth.Session, verify, setDefault bool) error {
	w := a.Out(c)
	var names []string
	for _, s := range sessions {
		ws, err := sessionFromImport(ctx, s, verify)
		name := importName(a.Config, ws, s)
		if err != nil {
			fmt.Fprintf(w, "! %s: %v\n", name, err)
			continue
		}
		a.Config.Set(name, ws)
		names = append(names, name)
		fmt.Fprintf(w, "✓ %s  %s (%s)  as %s\n", name, ws.TeamName, ws.TeamID, firstNonEmpty(ws.User, ws.Kind()))
	}
	if len(names) == 0 {
		return errors.New("imported no workspaces")
	}
	if (setDefault || a.Config.Workspace == "") && len(names) > 0 {
		a.Config.Workspace = names[0]
	}
	if err := a.Config.Save(); err != nil {
		return err
	}
	fmt.Fprintf(w, "Saved %d workspace(s) to %s (default: %s)\n", len(names), a.Config.File(), a.Config.Workspace)
	return nil
}

// importName picks a stable workspace name: the domain, else team id.
func importName(cfg *config.Config, ws config.Workspace, s localauth.Session) string {
	if n, ok := cfg.FindTeam(ws.TeamID); ok {
		return n
	}
	for _, v := range []string{ws.Domain, s.Domain, ws.TeamID, s.TeamID} {
		if v != "" {
			return v
		}
	}
	return "workspace"
}

func authAddCommand() *cli.Command {
	return &cli.Command{
		Name:      "add",
		Usage:     "Add a workspace from a pasted token",
		ArgsUsage: "[name]",
		Description: `Save a token. With no --token, slack-cli prompts for it (hidden).

  xoxp- / xoxb-  OAuth user or bot token (no cookie needed).
  xoxc-          Desktop/browser session token; pass --cookie xoxd-... too.

slack-cli calls auth.test to fill in the team and user, and names the
workspace by its domain unless you pass a name.`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "token", Usage: "Token (xoxp-/xoxb-/xoxc-); '-' reads stdin"},
			&cli.StringFlag{Name: "cookie", Usage: `The "d" cookie (xoxd-...) for an xoxc- token`},
			&cli.StringFlag{Name: "app-token", Usage: "App-level token (xapp-...) for Socket Mode listen"},
			&cli.StringFlag{Name: "url", Usage: "Workspace URL (for an xoxc- token on Enterprise Grid)"},
			&cli.BoolFlag{Name: "no-verify", Usage: "Skip the auth.test check"},
			&cli.BoolFlag{Name: "set-default", Usage: "Make this the default workspace"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			token := c.String("token")
			if token == "" || token == "-" {
				if token == "" {
					fmt.Fprint(a.Out(c), "Paste token (hidden): ")
				}
				var err error
				if token, err = readSecret(); err != nil {
					return err
				}
			}
			token = strings.TrimSpace(token)
			if token == "" {
				return errors.New("token is required")
			}
			ws := config.Workspace{
				Token: token, Cookie: strings.TrimSpace(c.String("cookie")),
				AppToken: strings.TrimSpace(c.String("app-token")),
				URL:      strings.TrimSpace(c.String("url")), Source: config.SourceToken,
			}
			if config.TokenKind(token) == "session" && ws.Cookie == "" {
				return errors.New("an xoxc- token needs --cookie xoxd-...")
			}
			if ws.URL != "" {
				ws.Domain = domainFromURL(ws.URL)
			}
			if !c.Bool("no-verify") {
				if _, err := verifyWorkspace(ctx, &ws); err != nil {
					return fmt.Errorf("token check failed (nothing saved): %w", err)
				}
			}
			name := strings.TrimSpace(c.Args().First())
			if name == "" {
				name = firstNonEmpty(ws.Domain, ws.TeamID, "workspace")
			}
			a.Config.Set(name, ws)
			if c.Bool("set-default") || a.Config.Workspace == "" {
				a.Config.Workspace = name
			}
			if err := a.Config.Save(); err != nil {
				return err
			}
			fmt.Fprintf(a.Out(c), "✓ saved workspace %s (%s, %s) to %s\n", name, ws.TeamName, ws.Kind(), a.Config.File())
			return nil
		},
	}
}

func authListCommand() *cli.Command {
	return &cli.Command{
		Name:    "list",
		Aliases: []string{"ls"},
		Usage:   "List configured workspaces",
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			if a.JSON() {
				type row struct {
					Name, TeamID, Team, Domain, Kind, Source string
					Default                                  bool
				}
				var rows []row
				for _, n := range a.Config.Names() {
					w := a.Config.Workspaces[n]
					rows = append(rows, row{n, w.TeamID, w.TeamName, w.Domain, w.Kind(), w.Source, n == a.Config.Workspace})
				}
				return output.WriteJSON(a.Out(c), rows)
			}
			var rows [][]string
			for _, n := range a.Config.Names() {
				w := a.Config.Workspaces[n]
				mark := ""
				if n == a.Config.Workspace {
					mark = "*"
				}
				rows = append(rows, []string{mark, n, w.TeamName, w.TeamID, w.Kind(), w.Source})
			}
			if len(rows) == 0 {
				fmt.Fprintln(a.Out(c), "(no workspaces — "+config.LoginHint()+")")
				return nil
			}
			return output.Table(a.Out(c), []string{"", "NAME", "TEAM", "TEAM_ID", "KIND", "SOURCE"}, rows)
		},
	}
}

func authUseCommand() *cli.Command {
	return &cli.Command{
		Name:      "use",
		Usage:     "Set the default workspace",
		ArgsUsage: "<name>",
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			key := strings.TrimSpace(c.Args().First())
			if key == "" {
				return errors.New("expected <name>")
			}
			name, _, ok := a.Config.Find(key)
			if !ok {
				return fmt.Errorf("no workspace %q (known: %s)", key, strings.Join(a.Config.Names(), ", "))
			}
			a.Config.Workspace = name
			if err := a.Config.Save(); err != nil {
				return err
			}
			fmt.Fprintf(a.Out(c), "✓ default workspace is now %s\n", name)
			return nil
		},
	}
}

func authRemoveCommand() *cli.Command {
	return &cli.Command{
		Name:      "remove",
		Aliases:   []string{"rm"},
		Usage:     "Delete a saved workspace",
		ArgsUsage: "<name>",
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			key := strings.TrimSpace(c.Args().First())
			name, _, ok := a.Config.Find(key)
			if !ok {
				return fmt.Errorf("no workspace %q", key)
			}
			a.Config.Remove(name)
			if err := a.Config.Save(); err != nil {
				return err
			}
			fmt.Fprintf(a.Out(c), "✓ removed workspace %s\n", name)
			return nil
		},
	}
}

func authTestCommand() *cli.Command {
	return &cli.Command{
		Name:  "test",
		Usage: "Check the selected workspace token (auth.test)",
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			cl, err := a.Client()
			if err != nil {
				return err
			}
			name, _, _ := a.Workspace()
			var at authTest
			if err := cl.CallJSON(ctx, "auth.test", url.Values{}, &at); err != nil {
				return err
			}
			if a.JSON() {
				return output.WriteJSON(a.Out(c), at)
			}
			fmt.Fprintf(a.Out(c), "✓ %s — %s (%s) as %s (%s)\n", name, at.Team, at.TeamID, at.User, at.UserID)
			fmt.Fprintf(a.Out(c), "  %s\n", at.URL)
			return nil
		},
	}
}

func authShowCommand() *cli.Command {
	return &cli.Command{
		Name:  "show",
		Usage: "Print the selected workspace (token masked)",
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			name, w, err := a.Workspace()
			if err != nil {
				return err
			}
			out := a.Out(c)
			fmt.Fprintf(out, "Config:    %s\n", a.Config.File())
			fmt.Fprintf(out, "Name:      %s%s\n", name, map[bool]string{true: " (default)"}[name == a.Config.Workspace])
			fmt.Fprintf(out, "Team:      %s (%s)\n", w.TeamName, w.TeamID)
			fmt.Fprintf(out, "Domain:    %s\n", w.Domain)
			fmt.Fprintf(out, "URL:       %s\n", w.URL)
			fmt.Fprintf(out, "User:      %s (%s)\n", w.User, w.UserID)
			fmt.Fprintf(out, "Kind:      %s (source: %s)\n", w.Kind(), w.Source)
			fmt.Fprintf(out, "Token:     %s\n", config.MaskSecret(w.Token))
			if w.Cookie != "" {
				fmt.Fprintf(out, "Cookie:    %s\n", config.MaskSecret(w.Cookie))
			}
			if w.AppToken != "" {
				fmt.Fprintf(out, "App token: %s\n", config.MaskSecret(w.AppToken))
			}
			return nil
		},
	}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func readSecret() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		r := bufio.NewReader(os.Stdin)
		s, err := r.ReadString('\n')
		return strings.TrimRight(s, "\r\n"), err
	}
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(b), err
}
