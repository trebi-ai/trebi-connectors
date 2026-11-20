package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	cli "github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/output"
)

// AuthCommand returns the `auth` command tree.
func AuthCommand() *cli.Command {
	return &cli.Command{
		Name:  "auth",
		Usage: "Authenticate with Plane (API key, host, default workspace)",
		Description: `plane-cli sends a personal access token in the X-Api-Key header.
Create one in Plane under Profile Settings → Personal Access Tokens.

Plane Cloud uses https://api.plane.so. For a self-hosted instance, set --host
to your instance URL.

Most commands need a workspace slug (the "my-team" in app.plane.so/my-team/).
Project commands also need a project: its UUID or identifier (for example ENG).

A token works only in the workspace that made it. For more workspaces, save a
key for each one with "auth workspace add <slug>". With --workspace/-w (or
PLANE_WORKSPACE) set to that slug, the CLI uses its key.

Config: ~/.cli-tools/plane-cli/config.toml (mode 0600).
Resolution order on every call: flag → env var → workspace entry → top-level config value.

With TREBI_STATE_DIR set, Trebi sets the key and the host. The CLI reads no
config file, and login, logout, and workspace add and remove fail.`,
		Commands: []*cli.Command{
			savesSetting(authLoginCommand()),
			authStatusCommand(),
			savesSetting(authLogoutCommand()),
			authShowCommand(),
			authWorkspaceCommand(),
		},
	}
}

// savesSetting makes a command that writes the config fail in Trebi mode:
// the settings come from the connection.
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

func authLoginCommand() *cli.Command {
	return &cli.Command{
		Name:  "login",
		Usage: "Save the API key, host, and default workspace/project",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "api-key", Usage: "API key (non-interactive)"},
			&cli.StringFlag{Name: "host", Usage: "API host (default https://api.plane.so)"},
			&cli.StringFlag{Name: "workspace", Usage: "Default workspace slug"},
			&cli.StringFlag{Name: "project", Usage: "Default project id or identifier"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			cfg, err := config.LoadFile()
			if err != nil {
				return err
			}
			set := false
			for _, f := range []struct {
				flag string
				dst  *string
			}{{"api-key", &cfg.APIKey}, {"host", &cfg.Host}, {"workspace", &cfg.Workspace}, {"project", &cfg.Project}} {
				if c.IsSet(f.flag) {
					*f.dst = strings.TrimSpace(c.String(f.flag))
					set = true
				}
			}
			if !set {
				r := bufio.NewReader(os.Stdin)
				if err := prompt(r, "Host", &cfg.Host, config.DefaultHost); err != nil {
					return err
				}
				current := config.MaskSecret(cfg.APIKey)
				if current == "" {
					current = "not set"
				}
				fmt.Printf("API key [%s] (input hidden): ", current)
				secret, err := readSecret()
				if err != nil {
					return err
				}
				if secret = strings.TrimSpace(secret); secret != "" {
					cfg.APIKey = secret
				}
				if err := prompt(r, "Default workspace slug", &cfg.Workspace, ""); err != nil {
					return err
				}
				if err := prompt(r, "Default project (id or identifier, optional)", &cfg.Project, ""); err != nil {
					return err
				}
			}
			if cfg.APIKey == "" {
				return errors.New("api key is required")
			}
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			fmt.Printf("Saved config to %s\n", cfg.File())
			return nil
		},
	}
}

func prompt(r *bufio.Reader, label string, dst *string, def string) error {
	cur := *dst
	if cur == "" {
		cur = def
	}
	fmt.Printf("%s [%s]: ", label, cur)
	in, err := readLine(r)
	if err != nil {
		return err
	}
	if in = strings.TrimSpace(in); in != "" {
		*dst = in
	}
	return nil
}

func authStatusCommand() *cli.Command {
	return &cli.Command{
		Name:  "status",
		Usage: "Check that the API key works (GET /api/v2/users/me/)",
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			cl, err := a.Client()
			if err != nil {
				return err
			}
			var me struct {
				Email       string `json:"email"`
				DisplayName string `json:"display_name"`
			}
			if err := cl.GetJSON(ctx, "/api/v2/users/me/", nil, &me); err != nil {
				return fmt.Errorf("auth failed: %w", err)
			}
			fmt.Printf("OK — %s as %s <%s>\n", a.Config.BaseURL(), me.DisplayName, me.Email)
			if a.Config.Workspace != "" {
				fmt.Printf("Workspace: %s\n", a.Config.Workspace)
			}
			if a.Config.Project != "" {
				fmt.Printf("Project:   %s\n", a.Config.Project)
			}
			return nil
		},
	}
}

func authLogoutCommand() *cli.Command {
	return &cli.Command{
		Name:  "logout",
		Usage: "Delete the saved config",
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			if err := a.Config.Delete(); err != nil {
				return err
			}
			fmt.Printf("Deleted %s\n", a.Config.File())
			return nil
		},
	}
}

func authShowCommand() *cli.Command {
	return &cli.Command{
		Name:  "show",
		Usage: "Print the resolved host, workspace, project, and masked API key",
		Action: func(ctx context.Context, c *cli.Command) error {
			a := FromCmd(c)
			fmt.Printf("Config file: %s\n", a.Config.File())
			fmt.Printf("Host:        %s\n", a.Config.BaseURL())
			fmt.Printf("Workspace:   %s\n", a.Config.Workspace)
			fmt.Printf("Project:     %s\n", a.Config.Project)
			fmt.Printf("API key:     %s\n", config.MaskSecret(a.Config.APIKey))
			for slug, w := range a.Config.Workspaces {
				fmt.Printf("Workspace key: %s %s\n", slug, config.MaskSecret(w.APIKey))
			}
			return nil
		},
	}
}

func readLine(r *bufio.Reader) (string, error) {
	s, err := r.ReadString('\n')
	if err != nil && s == "" {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

func readSecret() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return readLine(bufio.NewReader(os.Stdin))
	}
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func authWorkspaceCommand() *cli.Command {
	return &cli.Command{
		Name:  "workspace",
		Usage: "Save an API key for one workspace",
		Commands: []*cli.Command{
			savesSetting(&cli.Command{
				Name:      "add",
				Usage:     "Save the API key (and host) of a workspace; prompts for the key when --api-key is not set",
				ArgsUsage: "<slug>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "api-key", Usage: "API key made in this workspace ('-' reads stdin)"},
					&cli.StringFlag{Name: "host", Usage: "API host, when it is not the top-level host"},
					&cli.BoolFlag{Name: "no-verify", Usage: "Save without a test request"},
				},
				Action: func(ctx context.Context, c *cli.Command) error {
					slug := strings.TrimSpace(c.Args().First())
					if slug == "" || c.Args().Len() > 1 {
						return errors.New("expected <slug>")
					}
					cfg, err := config.LoadFile()
					if err != nil {
						return err
					}
					w := cfg.Workspaces[slug]
					if c.IsSet("host") {
						w.Host = strings.TrimSpace(c.String("host"))
					}
					key := c.String("api-key")
					if key == "" || key == "-" {
						if key == "" {
							fmt.Printf("API key for %s (input hidden): ", slug)
						}
						if key, err = readSecret(); err != nil {
							return err
						}
					}
					if w.APIKey = strings.TrimSpace(key); w.APIKey == "" {
						return errors.New("api key is required")
					}
					if !c.Bool("no-verify") {
						test := &config.Config{APIKey: w.APIKey, Host: w.Host}
						if test.Host == "" {
							test.Host = FromCmd(c).Config.Host
						}
						path := "/api/v2/workspaces/" + url.PathEscape(slug) + "/projects/"
						if _, err := client.New(test).Do(ctx, client.Request{Method: http.MethodGet, Path: path, Query: url.Values{"per_page": {"1"}}}); err != nil {
							return fmt.Errorf("key does not work in workspace %q (nothing saved): %w", slug, err)
						}
					}
					cfg.SetWorkspace(slug, w)
					if err := cfg.Save(); err != nil {
						return err
					}
					fmt.Printf("Saved the key for workspace %s in %s\n", slug, cfg.File())
					return nil
				},
			}),
			{
				Name:  "list",
				Usage: "List the workspaces with a saved key",
				Action: func(ctx context.Context, c *cli.Command) error {
					cfg, err := config.LoadFile()
					if err != nil {
						return err
					}
					rows := [][]string{}
					if cfg.APIKey != "" {
						// The top-level key belongs to the default workspace.
						name := cfg.Workspace
						if name == "" {
							name = "-"
						}
						rows = append(rows, []string{name, "yes", config.MaskSecret(cfg.APIKey), cfg.Host})
					}
					slugs := make([]string, 0, len(cfg.Workspaces))
					for slug := range cfg.Workspaces {
						slugs = append(slugs, slug)
					}
					sort.Strings(slugs)
					for _, slug := range slugs {
						w := cfg.Workspaces[slug]
						rows = append(rows, []string{slug, "", config.MaskSecret(w.APIKey), w.Host})
					}
					return output.Table(c.Root().Writer, []string{"WORKSPACE", "DEFAULT", "API KEY", "HOST"}, rows)
				},
			},
			savesSetting(&cli.Command{
				Name:      "remove",
				Usage:     "Delete the saved key of a workspace",
				ArgsUsage: "<slug>",
				Action: func(ctx context.Context, c *cli.Command) error {
					slug := c.Args().First()
					cfg, err := config.LoadFile()
					if err != nil {
						return err
					}
					if _, ok := cfg.Workspaces[slug]; !ok {
						return fmt.Errorf("no saved key for workspace %q", slug)
					}
					delete(cfg.Workspaces, slug)
					if err := cfg.Save(); err != nil {
						return err
					}
					fmt.Printf("Removed the key for workspace %s\n", slug)
					return nil
				},
			}),
		},
	}
}
