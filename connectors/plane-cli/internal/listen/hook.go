package listen

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/client"
)

// hookPrefix starts the name of each webhook that listen creates. The name
// also has the host and the process id, to find webhooks of stopped runs.
const hookPrefix = "plane-cli listen "

// Hook is a temporary workspace webhook.
type Hook struct {
	cl        *client.Client
	workspace string
	ID        string
	Secret    string
}

// CreateHook creates an active v2 webhook to target for the scopes.
func CreateHook(ctx context.Context, cl *client.Client, workspace, target string, scopes []string) (*Hook, error) {
	host, _ := os.Hostname()
	body := map[string]any{
		"name":         fmt.Sprintf("%s%s %d", hookPrefix, host, os.Getpid()),
		"url":          target,
		"scopes":       scopes,
		"is_active":    true,
		"content_type": "application/json",
	}
	resp, err := cl.Do(ctx, client.Request{Method: http.MethodPost, Path: hooksPath(workspace), Query: url.Values{"fields": {"all"}}, Body: body})
	if err != nil {
		return nil, fmt.Errorf("create webhook: %w", err)
	}
	h := &Hook{cl: cl, workspace: workspace}
	var out struct {
		ID        string `json:"id"`
		SecretKey string `json:"secret_key"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil || out.ID == "" {
		return nil, fmt.Errorf("create webhook: unexpected response %s", resp.Body)
	}
	h.ID, h.Secret = out.ID, out.SecretKey
	if h.Secret == "" {
		// Older servers return the secret only from regenerate.
		r, err := cl.Do(ctx, client.Request{Method: http.MethodPost, Path: hooksPath(workspace) + h.ID + "/regenerate/", Query: url.Values{"fields": {"secret_key"}}})
		if err == nil {
			_ = json.Unmarshal(r.Body, &out)
			h.Secret = out.SecretKey
		}
		if h.Secret == "" {
			_ = h.Delete(ctx)
			return nil, fmt.Errorf("create webhook: no secret key in the response")
		}
	}
	return h, nil
}

// Delete removes the webhook.
func (h *Hook) Delete(ctx context.Context) error {
	_, err := h.cl.Do(ctx, client.Request{Method: http.MethodDelete, Path: hooksPath(h.workspace) + h.ID + "/"})
	return err
}

// DeleteStaleHooks removes webhooks that an earlier listen run on this host
// did not delete (for example after a crash). It returns their count.
func DeleteStaleHooks(ctx context.Context, cl *client.Client, workspace string) (int, error) {
	items, err := cl.ListAll(ctx, hooksPath(workspace), url.Values{"fields": {"id,name"}}, true, 0)
	if err != nil {
		return 0, err
	}
	host, _ := os.Hostname()
	n := 0
	for _, raw := range items {
		var w struct{ ID, Name string }
		if json.Unmarshal(raw, &w) != nil || !strings.HasPrefix(w.Name, hookPrefix+host+" ") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimPrefix(w.Name, hookPrefix+host+" "))
		if err != nil || processAlive(pid) {
			continue
		}
		h := &Hook{cl: cl, workspace: workspace, ID: w.ID}
		if err := h.Delete(ctx); err == nil {
			n++
		}
	}
	return n, nil
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func hooksPath(workspace string) string {
	return "/api/v2/workspaces/" + url.PathEscape(workspace) + "/webhooks/"
}
