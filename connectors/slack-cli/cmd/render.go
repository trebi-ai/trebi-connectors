package cmd

import (
	"context"
	"encoding/json"
	"io"

	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/output"
)

// renderTable renders a response body as a table, picking the list field
// when there is one and otherwise a key/value block.
func (a *AppContext) renderTable(w io.Writer, body []byte) error {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return output.WriteRaw(w, body)
	}
	if page, ok := client.ParsePage(body); ok && len(m) > 1 {
		if err := output.Rows(w, rawObjects(page.Items), a.Columns); err != nil {
			return err
		}
		if page.NextCursor != "" && !a.JSON() {
			io.WriteString(w, "(more: pass --all)\n")
		}
		return nil
	}
	// A single object: drop the "ok" envelope field and show the rest.
	obj := map[string]any{}
	for k, raw := range m {
		if k == "ok" {
			continue
		}
		var v any
		_ = json.Unmarshal(raw, &v)
		obj[k] = v
	}
	// A wrapper with one object field (for example {"user":{...}}) shows that.
	if len(obj) == 1 {
		for _, v := range obj {
			if inner, ok := v.(map[string]any); ok {
				return output.Object(w, inner, a.Columns)
			}
		}
	}
	return output.Object(w, obj, a.Columns)
}

// RenderItems writes a list collected over many pages.
func (a *AppContext) RenderItems(w io.Writer, items []json.RawMessage) error {
	if a.JSON() || a.Raw {
		if items == nil {
			items = []json.RawMessage{}
		}
		arr := make([]json.RawMessage, len(items))
		copy(arr, items)
		return output.WriteJSON(w, arr)
	}
	return output.Rows(w, rawObjects(items), a.Columns)
}

func rawObjects(items []json.RawMessage) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			var x any
			_ = json.Unmarshal(raw, &x)
			m = map[string]any{"value": x}
		}
		out = append(out, m)
	}
	return out
}

// refreshSession mints a fresh xoxc- token from the saved cookie and saves it.
func (a *AppContext) refreshSession(ctx context.Context, name string, w config.Workspace) (config.Workspace, error) {
	cl := client.New(w)
	token, err := mintToken(ctx, cl.HTTP(), w)
	if err != nil {
		return config.Workspace{}, err
	}
	w.Token = token
	a.Config.Set(name, w)
	a.ws = w
	if config.Trebi() {
		return w, nil // serve is the one writer of the state folder
	}
	if err := a.Config.Save(); err != nil {
		return config.Workspace{}, err
	}
	return w, nil
}
