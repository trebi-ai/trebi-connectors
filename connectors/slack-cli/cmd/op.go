package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	cli "github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/serve"
)

// OpCommand holds the read-only operations of the Trebi connection. Each
// one reads a JSON object on stdin and writes JSON on stdout.
func OpCommand() *cli.Command {
	return &cli.Command{
		Name:  "op",
		Usage: "Read-only operations for Trebi: JSON in on stdin, JSON out",
		Description: `Each operation reads one JSON object on stdin and writes one JSON object.
The messages have the shape of the events of ` + "`slack-cli serve`" + `.

The workspace is the "workspace" field, else the first of SLACK_WORKSPACES,
else --workspace, else the default workspace.

  echo '{"query":"deploy in:#ops","limit":10}' | slack-cli op search
  echo '{"room":"T123/C456","limit":20}' | slack-cli op history`,
		Commands: []*cli.Command{
			{
				Name:  "search",
				Usage: `Search messages: {"query", "workspace"?, "limit"? (1-100, default 20)}`,
				Action: func(ctx context.Context, c *cli.Command) error {
					var in struct {
						Query     string `json:"query"`
						Workspace string `json:"workspace"`
						Limit     int    `json:"limit"`
					}
					if err := readInput(c, &in); err != nil {
						return err
					}
					if strings.TrimSpace(in.Query) == "" {
						return errors.New("query is required")
					}
					team, _, err := opTeam(ctx, c, in.Workspace, "")
					if err != nil {
						return err
					}
					page, err := team.Search(ctx, in.Query, in.Limit)
					if err != nil {
						return err
					}
					return writeOutput(c, page)
				},
			},
			{
				Name:  "history",
				Usage: `Read a channel or a thread: {"room", "thread"?, "before"?, "after"?, "limit"? (1-200, default 50), "workspace"?}`,
				Action: func(ctx context.Context, c *cli.Command) error {
					var in struct {
						Room      string `json:"room"`
						Thread    string `json:"thread"`
						Before    string `json:"before"`
						After     string `json:"after"`
						Limit     int    `json:"limit"`
						Workspace string `json:"workspace"`
					}
					if err := readInput(c, &in); err != nil {
						return err
					}
					teamID, ch, ok := strings.Cut(in.Room, "/")
					if !ok {
						teamID, ch = "", in.Room
					}
					if strings.TrimSpace(ch) == "" {
						return errors.New("room is required: a room id <team>/<channel>, a channel id, or #name")
					}
					team, dir, err := opTeam(ctx, c, in.Workspace, teamID)
					if err != nil {
						return err
					}
					if !client.IsChannelID(ch) {
						if ch, err = dir.ResolveChannel(ctx, ch); err != nil {
							return err
						}
					}
					page, err := team.History(ctx, serve.HistoryQuery{Channel: ch, Thread: in.Thread, Before: in.Before, After: in.After, Limit: in.Limit})
					if err != nil {
						return err
					}
					return writeOutput(c, page)
				},
			},
		},
	}
}

// opTeam selects the workspace of an operation: the team id of a room, the
// workspace field, the first of SLACK_WORKSPACES, then the global flag.
func opTeam(ctx context.Context, c *cli.Command, workspace, teamID string) (*serve.Team, *client.Directory, error) {
	a := FromCmd(c)
	switch {
	case teamID != "":
		name, ok := a.Config.FindTeam(teamID)
		if !ok {
			return nil, nil, fmt.Errorf("no workspace with team id %s on this computer", teamID)
		}
		a.WorkspaceFlag = name
	case workspace != "":
		a.WorkspaceFlag = workspace
	case a.WorkspaceFlag == "":
		if names, _ := a.Config.Pick(os.Getenv(EnvWorkspaces)); len(names) > 0 {
			a.WorkspaceFlag = names[0]
		}
	}
	cl, err := a.Client()
	if err != nil {
		return nil, nil, err
	}
	dir, err := a.Directory()
	if err != nil {
		return nil, nil, err
	}
	name, w, _ := a.Workspace() //nolint:errcheck // Client selected it already
	team := serve.NewTeam(name, w, cl, dir)
	if team.ID == "" || team.SelfID == "" {
		var who struct {
			UserID string `json:"user_id"`
			TeamID string `json:"team_id"`
		}
		if err := cl.CallJSON(ctx, "auth.test", nil, &who); err != nil {
			return nil, nil, err
		}
		team.ID, team.SelfID = who.TeamID, who.UserID
	}
	return team, dir, nil
}

func readInput(c *cli.Command, v any) error {
	r := c.Root().Reader
	if r == nil {
		r = os.Stdin
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(b)) == "" {
		b = []byte("{}")
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("stdin: %w", err)
	}
	return nil
}

func writeOutput(c *cli.Command, v any) error {
	return json.NewEncoder(c.Root().Writer).Encode(v)
}
