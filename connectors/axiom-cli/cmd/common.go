package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/internal/output"
	"github.com/urfave/cli/v2"
)

var orgWarnOnce sync.Once

// flagSetByUser distinguishes a flag passed on the CLI from one whose value
// came via EnvVars. urfave/cli's IsSet returns true for env-sourced values.
// We detect "actually-on-the-CLI" by walking the parent lineage and checking
// IsSet against env values.
func flagSetByUser(c *cli.Context, name, envName string) bool {
	envVal, envHas := os.LookupEnv(envName)
	for ctx := c; ctx != nil; ctx = parent(ctx) {
		if ctx.IsSet(name) {
			if !envHas {
				return true
			}
			if ctx.String(name) != envVal {
				return true
			}
			// IsSet was true but the value matches env; could be either.
			// Treat as not user-set so source resolution shows "env".
			return false
		}
	}
	return false
}

func parent(c *cli.Context) *cli.Context {
	lineage := c.Lineage()
	if len(lineage) >= 2 {
		return lineage[1]
	}
	return nil
}

// resolved walks parent contexts so app-level flags are visible to subcommands.
func resolved(c *cli.Context) (*config.Resolved, error) {
	flagToken := c.String("token")
	flagOrgID := c.String("org-id")
	flagAPIURL := c.String("api-url")

	envToken, envTokenSet := os.LookupEnv(config.EnvToken)
	envOrgID := os.Getenv(config.EnvOrgID)
	envAPIURL := os.Getenv(config.EnvAPIURL)

	in := config.ResolveInput{
		FlagSet:       flagSetByUser(c, "token", config.EnvToken),
		FlagToken:     flagToken,
		FlagOrgID:     flagOrgID,
		FlagAPIURL:    flagAPIURL,
		FlagOrgIDSet:  flagSetByUser(c, "org-id", config.EnvOrgID),
		FlagAPIURLSet: flagSetByUser(c, "api-url", config.EnvAPIURL),
		EnvToken:      envToken,
		EnvTokenSet:   envTokenSet,
		EnvOrgID:      envOrgID,
		EnvAPIURL:     envAPIURL,
	}
	return config.Resolve(in)
}

// newClient builds a *client.Client from resolved config.
func newClient(c *cli.Context) (*client.Client, error) {
	r, err := resolved(c)
	if err != nil {
		return nil, err
	}
	if r.Token == "" {
		if r.Trebi {
			return nil, fmt.Errorf("no token: set the API token in the Trebi connection")
		}
		return nil, fmt.Errorf("no token configured: set --token, AXIOM_TOKEN, or run `axiom-cli auth set`")
	}
	if strings.HasPrefix(r.Token, "xapt-") && r.OrgID == "" {
		orgWarnOnce.Do(func() {
			fmt.Fprintln(os.Stderr, "warning: personal access token detected but no --org-id / AXIOM_ORG_ID set; calls may fail")
		})
	}
	return client.New(r.APIURL, r.Token, r.OrgID), nil
}

// emit writes the response body using the --compact flag from anywhere up the context tree.
func emit(c *cli.Context, raw json.RawMessage) error {
	return output.WriteJSON(c.App.Writer, raw, c.Bool("compact"))
}

// readBody loads a JSON request body from the named flag. Accepts:
//   - "-"           → read from stdin
//   - "@/some/path" → read from file
//   - inline JSON   → use as-is
//
// The body is validated as JSON before returning so users get fast feedback.
func readBody(c *cli.Context, flagName string) (json.RawMessage, error) {
	v := c.String(flagName)
	if v == "" {
		return nil, fmt.Errorf("--%s is required", flagName)
	}
	var raw []byte
	var err error
	switch {
	case v == "-":
		raw, err = io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
	case strings.HasPrefix(v, "@"):
		raw, err = os.ReadFile(v[1:])
		if err != nil {
			return nil, fmt.Errorf("read body file: %w", err)
		}
	default:
		raw = []byte(v)
	}
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("--%s is not valid JSON: %w", flagName, err)
	}
	return json.RawMessage(raw), nil
}

// ctxOf returns the cli.Context's underlying context, falling back to background.
func ctxOf(c *cli.Context) context.Context {
	if c.Context != nil {
		return c.Context
	}
	return context.Background()
}

// bodyFlag is the standard --body flag used by create/update commands.
func bodyFlag() cli.Flag {
	return &cli.StringFlag{
		Name:     "body",
		Usage:    "JSON body: inline, @path/to/file, or - for stdin",
		Required: true,
	}
}

func idFlag() cli.Flag {
	return &cli.StringFlag{Name: "id", Usage: "Resource id", Required: true}
}

// --- Action factories used across resource commands ---

// listAction returns an ActionFunc that GETs path. Each name in queryFlags is
// looked up via c.String and, if non-empty, attached as a query parameter
// using the same name (replace dashes with the canonical key as you go in
// callers if the API expects a different key).
func listAction(path string, queryFlags ...string) cli.ActionFunc {
	return func(c *cli.Context) error {
		client, err := newClient(c)
		if err != nil {
			return err
		}
		q := buildQuery(c, queryFlags)
		raw, err := client.Get(ctxOf(c), path, q)
		if err != nil {
			return err
		}
		return emit(c, raw)
	}
}

// getByID returns an ActionFunc that GETs prefix + "/" + --id.
func getByID(prefix string, queryFlags ...string) cli.ActionFunc {
	return func(c *cli.Context) error {
		id := c.String("id")
		if id == "" {
			return fmt.Errorf("--id is required")
		}
		client, err := newClient(c)
		if err != nil {
			return err
		}
		q := buildQuery(c, queryFlags)
		raw, err := client.Get(ctxOf(c), prefix+"/"+id, q)
		if err != nil {
			return err
		}
		return emit(c, raw)
	}
}

// postBody returns an ActionFunc that POSTs --body to path.
func postBody(path string) cli.ActionFunc {
	return func(c *cli.Context) error {
		body, err := readBody(c, "body")
		if err != nil {
			return err
		}
		client, err := newClient(c)
		if err != nil {
			return err
		}
		raw, err := client.Post(ctxOf(c), path, body)
		if err != nil {
			return err
		}
		return emit(c, raw)
	}
}

// putByID returns an ActionFunc that PUTs --body to prefix + "/" + --id.
func putByID(prefix string) cli.ActionFunc {
	return func(c *cli.Context) error {
		id := c.String("id")
		if id == "" {
			return fmt.Errorf("--id is required")
		}
		body, err := readBody(c, "body")
		if err != nil {
			return err
		}
		client, err := newClient(c)
		if err != nil {
			return err
		}
		raw, err := client.Put(ctxOf(c), prefix+"/"+id, body)
		if err != nil {
			return err
		}
		return emit(c, raw)
	}
}

// deleteByID returns an ActionFunc that DELETEs prefix + "/" + --id.
func deleteByID(prefix string) cli.ActionFunc {
	return func(c *cli.Context) error {
		id := c.String("id")
		if id == "" {
			return fmt.Errorf("--id is required")
		}
		client, err := newClient(c)
		if err != nil {
			return err
		}
		raw, err := client.Delete(ctxOf(c), prefix+"/"+id)
		if err != nil {
			return err
		}
		return emit(c, raw)
	}
}

func buildQuery(c *cli.Context, names []string) url.Values {
	q := url.Values{}
	for _, n := range names {
		if v := c.String(n); v != "" {
			q.Set(n, v)
		}
	}
	return q
}
