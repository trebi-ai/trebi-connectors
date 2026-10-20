package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	cli "github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/output"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/spec"
)

// reserved holds flag names used by the root or by every method command.
var reserved = map[string]bool{
	"workspace": true, "output": true, "json": true, "raw": true, "columns": true,
	"verbose": true, "no-resolve": true, "help": true, "version": true,
	"data": true, "set": true, "all": true, "max": true, "dry-run": true,
}

// channelParams are params that name a conversation; the CLI resolves a
// "#name" or "@user" value to an id.
var channelParams = map[string]bool{"channel": true, "channel_id": true}

// userParams are params that name a user; the CLI resolves a "@name" or
// email value to an id.
var userParams = map[string]bool{"user": true, "user_id": true}

// flagParam is a spec param with its CLI flag name.
type flagParam struct {
	spec.Param
	flag string
}

// methodCommand turns one spec method into a CLI leaf.
type methodCommand struct {
	method spec.Method
	params []flagParam
}

func newMethodCommand(m spec.Method) *methodCommand {
	mc := &methodCommand{method: m}
	used := map[string]bool{}
	for _, p := range m.Params {
		name := strings.ReplaceAll(p.Name, "_", "-")
		if reserved[name] || used[name] {
			name = "p-" + name
		}
		used[name] = true
		mc.params = append(mc.params, flagParam{Param: p, flag: name})
	}
	return mc
}

// Command builds the urfave command.
func (mc *methodCommand) Command() *cli.Command {
	m := mc.method
	leaf := m.Path[len(m.Path)-1]
	var desc strings.Builder
	if m.Summary != "" {
		desc.WriteString(m.Summary + "\n\n")
	}
	fmt.Fprintf(&desc, "Slack method: %s (%s)\n", m.Name, m.HTTP)
	if s := scopeLine(m); s != "" {
		desc.WriteString(s + "\n")
	}
	fmt.Fprintf(&desc, "Docs: %s\n", m.Doc)
	if m.Note != "" {
		fmt.Fprintf(&desc, "\nNote: %s\n", m.Note)
	}
	desc.WriteString("\nBody: typed flags, plus --data '<json>' (or @file, - for stdin) as the base,\nand --set key=value for any field.")
	return &cli.Command{
		Name:        leaf,
		Usage:       firstLine(m.Summary),
		Description: strings.TrimSpace(desc.String()),
		Flags:       mc.flags(),
		Action:      mc.run,
	}
}

func scopeLine(m spec.Method) string {
	var parts []string
	for _, kind := range []string{"user", "bot"} {
		if s := m.Scopes[kind]; len(s) > 0 {
			parts = append(parts, kind+": "+strings.Join(s, ", "))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Scopes — " + strings.Join(parts, " | ")
}

func (mc *methodCommand) flags() []cli.Flag {
	var flags []cli.Flag
	for _, p := range mc.params {
		usage := firstLine(p.Desc)
		if len(p.Enum) > 0 {
			usage += " (one of: " + strings.Join(p.Enum, ", ") + ")"
		}
		if p.Required {
			usage += " [required]"
		}
		if channelParams[p.Name] {
			usage += " (id, #name, @user, or email)"
		} else if userParams[p.Name] {
			usage += " (id, @name, or email)"
		}
		usage = strings.ReplaceAll(usage, "`", "'")
		switch p.Type {
		case spec.TypeBoolean:
			flags = append(flags, &cli.BoolFlag{Name: p.flag, Usage: usage})
		case spec.TypeCSV:
			flags = append(flags, &cli.StringSliceFlag{Name: p.flag, Usage: usage + " (repeatable or comma-separated)"})
		default:
			if p.Type == spec.TypeJSON {
				usage += " [JSON, @file, or -]"
			}
			flags = append(flags, &cli.StringFlag{Name: p.flag, Usage: usage})
		}
	}
	flags = append(flags,
		&cli.StringFlag{Name: "data", Aliases: []string{"d"}, Usage: "Request body as JSON (or @file, - for stdin); flags merge over it"},
		&cli.StringSliceFlag{Name: "set", Usage: "Body field as key=value (repeatable; value types are inferred)"},
	)
	if mc.method.Paginated() {
		flags = append(flags,
			&cli.BoolFlag{Name: "all", Aliases: []string{"a"}, Usage: "Follow pagination and return every item"},
			&cli.IntFlag{Name: "max", Usage: "With --all, stop after this many items (0 = no cap)"},
		)
	}
	flags = append(flags, &cli.BoolFlag{Name: "dry-run", Usage: "Print the request instead of sending it"})
	return flags
}

func (mc *methodCommand) run(ctx context.Context, c *cli.Command) error {
	a := FromCmd(c)
	params, err := mc.params0(c)
	if err != nil {
		return err
	}
	w := a.Out(c)
	if c.Bool("dry-run") {
		fmt.Fprintf(w, "POST %s%s\n", client.DefaultBaseURL, mc.method.Name)
		return output.WriteJSON(w, formToMap(params))
	}
	cl, err := a.Client()
	if err != nil {
		return err
	}
	if err := mc.resolveNames(ctx, a, params); err != nil {
		return err
	}
	if mc.method.Paginated() && c.Bool("all") {
		page, err := cl.ListAll(ctx, mc.method.Name, params, int(c.Int("max")))
		if err != nil {
			return err
		}
		return a.RenderItems(w, page.Items)
	}
	body, err := cl.Call(ctx, mc.method.Name, params)
	if err != nil {
		return err
	}
	return a.Render(w, body)
}

// params0 builds the form params from --data, typed flags, and --set.
func (mc *methodCommand) params0(c *cli.Command) (url.Values, error) {
	form := url.Values{}
	if raw := c.String("data"); raw != "" {
		v, err := parseJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("--data: %w", err)
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("--data must be a JSON object")
		}
		for k, val := range m {
			form.Set(k, toParam(val))
		}
	}
	for _, p := range mc.params {
		if !c.IsSet(p.flag) {
			continue
		}
		v, err := flagValue(c, p)
		if err != nil {
			return nil, err
		}
		form.Set(p.Name, v)
	}
	for _, kv := range c.StringSlice("set") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --set %q (expected key=value)", kv)
		}
		form.Set(k, v)
	}
	var missing []string
	for _, p := range mc.params {
		if p.Required && form.Get(p.Name) == "" {
			missing = append(missing, "--"+p.flag)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("missing required: %s (or pass them in --data)", strings.Join(missing, ", "))
	}
	return form, nil
}

// resolveNames turns channel and user names in the params into ids, unless
// --no-resolve is set.
func (mc *methodCommand) resolveNames(ctx context.Context, a *AppContext, form url.Values) error {
	if a.NoResolve {
		return nil
	}
	var dir *client.Directory
	get := func() (*client.Directory, error) {
		if dir == nil {
			d, err := a.Directory()
			if err != nil {
				return nil, err
			}
			dir = d
		}
		return dir, nil
	}
	for _, p := range mc.params {
		val := form.Get(p.Name)
		if val == "" {
			continue
		}
		switch {
		case channelParams[p.Name] && !client.IsChannelID(val):
			d, err := get()
			if err != nil {
				return err
			}
			id, err := d.ResolveChannel(ctx, val)
			if err != nil {
				return err
			}
			form.Set(p.Name, id)
		case userParams[p.Name] && !client.IsUserID(val):
			d, err := get()
			if err != nil {
				return err
			}
			id, err := d.ResolveUser(ctx, val)
			if err != nil {
				return err
			}
			form.Set(p.Name, id)
		}
	}
	return nil
}

// flagValue converts a flag to the string form its param type needs.
func flagValue(c *cli.Command, p flagParam) (string, error) {
	switch p.Type {
	case spec.TypeBoolean:
		return strconv.FormatBool(c.Bool(p.flag)), nil
	case spec.TypeCSV:
		var out []string
		for _, v := range c.StringSlice(p.flag) {
			for _, s := range strings.Split(v, ",") {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
		return strings.Join(out, ","), nil
	case spec.TypeJSON:
		v, err := parseJSON(c.String(p.flag))
		if err != nil {
			return "", fmt.Errorf("--%s: %w", p.flag, err)
		}
		return toParam(v), nil
	}
	return c.String(p.flag), nil
}

// toParam renders a JSON value the way Slack's form transport wants it:
// objects and arrays as JSON strings, scalars as plain text.
func toParam(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case nil:
		return ""
	}
	b, _ := json.Marshal(v) // objects/arrays → JSON string
	return string(b)
}

func formToMap(form url.Values) map[string]string {
	out := map[string]string{}
	for k := range form {
		out[k] = form.Get(k)
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
