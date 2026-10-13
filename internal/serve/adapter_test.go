package serve

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"

	"github.com/flarco/cli-tools/slack-cli/internal/client"
	"github.com/flarco/cli-tools/slack-cli/internal/fakeslack"
)

func sandboxTeam(t *testing.T) *Team {
	t.Helper()
	fake := fakeslack.Start()
	t.Cleanup(fake.Close)
	w := fake.Config().Workspaces["sandbox"]
	cl := client.New(w)
	cl.SetBaseURL(fake.APIURL())
	team := NewTeam("sandbox", w, cl, client.NewDirectory(cl, "", "sandbox"))
	if err := team.Listen(w, fake.WSURL()); err != nil {
		t.Fatal(err)
	}
	return team
}

func TestServeSandbox(t *testing.T) {
	c := sdktest.Start(New("test", []*Team{sandboxTeam(t)}, ""))
	defer c.Close() //nolint:errcheck // the test ends
	res, err := c.Initialize(sdk.InitializeParams{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Account == nil || res.Account.Name != fakeslack.TeamName || res.Account.ID != fakeslack.SelfID {
		t.Errorf("account %+v", res.Account)
	}
	want := []string{
		"T0SANDBOX/C0GENERAL/1767225600.000100",
		"T0SANDBOX/C0GENERAL/1767225600.000100.mention",
		"T0SANDBOX/C0GENERAL/1767225600.000200",
		"T0SANDBOX/D0ANA/1767225600.000300",
		"T0SANDBOX/C0TEAM/1767225600.000400",
		"T0SANDBOX/C0GENERAL/1767225600.000100.U0BO.+1",
		"T0SANDBOX/C0TEAM.join.U0BO.1767225600.000600",
		"T0SANDBOX/C0GENERAL/1767225600.000100.edit.1767225600.000700",
		"T0SANDBOX/C0GENERAL/1767225600.000200.delete",
		"T0SANDBOX/G0GROUP/1767225600.000900",
	}
	for _, id := range want {
		m, err := c.WaitNote(sdk.MethodEvent)
		if err != nil {
			t.Fatal(err)
		}
		var ev sdk.Event
		if err := json.Unmarshal(m.Params, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.ID != id {
			t.Errorf("event id %q, want %q", ev.ID, id)
		}
	}
}

func TestNoWorkspace(t *testing.T) {
	c := sdktest.Start(New("test", nil, "no Slack workspace"))
	defer c.Close() //nolint:errcheck // the test ends
	if _, err := c.Initialize(sdk.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	var st sdk.Status
	for st.Message == "" { // the first status comes from auth/status
		m, err := c.WaitNote(sdk.MethodStatus)
		if err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(m.Params, &st)
	}
	if st.State != sdk.StateAuthRequired || st.Message != "no Slack workspace" {
		t.Errorf("status %+v", st)
	}
}

func TestOps(t *testing.T) {
	team := sandboxTeam(t)
	ctx := context.Background()
	if err := team.check(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := team.History(ctx, HistoryQuery{Channel: fakeslack.GeneralID, Thread: "1767225600.000100"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 || page.Messages[1].ReplyTo != "T0SANDBOX/C0GENERAL/1767225600.000100" {
		t.Errorf("thread %+v", page.Messages)
	}
	page, err = team.History(ctx, HistoryQuery{Channel: fakeslack.DMID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Room.Kind != sdk.RoomDM || page.Messages[0].Room.Name != "Ana" {
		t.Errorf("dm %+v", page.Messages)
	}
	page, err = team.Search(ctx, "build", 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Messages) != 2 {
		t.Errorf("search %+v", page)
	}
}
