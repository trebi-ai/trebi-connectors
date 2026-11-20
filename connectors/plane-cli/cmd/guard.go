package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/client"
)

// guard is a check that runs before the request of one command. An error stops the request.
type guard func(ctx context.Context, a *AppContext, cl *client.Client, args []string, body any) error

// guards hold the checks by command path. --force skips them.
var guards = map[string]guard{
	"work-item update": closeGuard,
}

// closeGuard refuses a closed state on a work item with open sub-items.
func closeGuard(ctx context.Context, a *AppContext, cl *client.Client, args []string, body any) error {
	m, _ := body.(map[string]any)
	ref, _ := m["state_id"].(string)
	if ref == "" {
		ref, _ = m["state"].(string)
	}
	if ref == "" || len(args) == 0 {
		return nil
	}
	ws, err := a.Workspace()
	if err != nil {
		return err
	}
	project, err := projectOf(a, args)
	if err != nil {
		return err
	}
	group, err := cl.StateGroup(ctx, ws, project, ref)
	if err != nil || !client.ClosedGroup(group) {
		return err
	}
	key, open, err := cl.OpenSubItems(ctx, ws, args[len(args)-1])
	if err != nil || len(open) == 0 {
		return err
	}
	if len(open) == 1 {
		return fmt.Errorf("%s has 1 open sub-item: %s. Close it first, or add --force", key, open[0])
	}
	return fmt.Errorf("%s has %d open sub-items: %s. Close them first, or add --force", key, len(open), strings.Join(open, ", "))
}
