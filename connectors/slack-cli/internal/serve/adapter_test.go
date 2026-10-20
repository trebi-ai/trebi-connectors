package serve

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"

	"github.com/trebi-ai/trebi-connectors/sdk"
	"github.com/trebi-ai/trebi-connectors/sdk/sdktest"

	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/fakeslack"
	"github.com/trebi-ai/trebi-connectors/connectors/slack-cli/internal/localauth"
)

// Tokens of the second fake team.
const (
	ocralID    = "T0OCRAL"
	ocralToken = "xoxc-ocral"
)

func startFake(t *testing.T) *fakeslack.Server {
	t.Helper()
	fake := fakeslack.Start()
	t.Cleanup(fake.Close)
	fake.AddTeam(ocralToken, ocralID, "Ocral", "ocral")
	return fake
}

// newStore is a store in a temp folder with the workspaces ws.
func newStore(t *testing.T, ws map[string]config.Workspace) *Store {
	t.Helper()
	cfg := config.New(filepath.Join(t.TempDir(), "config.toml"))
	for n, w := range ws {
		cfg.Set(n, w)
	}
	return NewStore(cfg)
}

var (
	sandboxWS = config.Workspace{Token: "xoxc-sandbox", Cookie: "xoxd-sandbox", TeamID: fakeslack.TeamID, TeamName: fakeslack.TeamName, Domain: "sandbox", UserID: fakeslack.SelfID}
	ocralWS   = config.Workspace{Token: ocralToken, Cookie: "xoxd-ocral", TeamID: ocralID, TeamName: "Ocral", Domain: "ocral", UserID: fakeslack.SelfID}
)

func newAdapter(fake *fakeslack.Server, store *Store, local map[string]Local) *Adapter {
	return New(Options{
		Version: "test", Store: store, Local: local, WSURL: fake.WSURL(),
		Client: func(w config.Workspace) *client.Client {
			cl := client.New(w)
			cl.SetBaseURL(fake.APIURL())
			return cl
		},
	})
}

func start(t *testing.T, a *Adapter) *sdktest.Conn {
	t.Helper()
	c := sdktest.Start(a)
	t.Cleanup(func() { _ = c.Close() }) //nolint:errcheck // the test ends
	if _, err := c.Initialize(sdk.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	return c
}

// desktopFixture writes a Slack app folder: the workspace list, the tokens
// in Local Storage (LevelDB), and a plain "d" cookie in the cookie DB.
func desktopFixture(t *testing.T, sessions ...localauth.Session) *localauth.Desktop {
	t.Helper()
	dir := t.TempDir()
	root := map[string]map[string]any{}
	teams := map[string]map[string]string{}
	for _, s := range sessions {
		root[s.TeamID] = map[string]any{"id": s.TeamID, "name": s.TeamName, "domain": s.Domain, "url": "https://" + s.Domain + ".slack.com/"}
		teams[s.TeamID] = map[string]string{"token": s.Token}
	}
	write := func(name string, v any) {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("storage/root-state.json", map[string]any{"workspaces": root})

	db, err := leveldb.OpenFile(filepath.Join(dir, "Local Storage", "leveldb"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"teams": teams}) //nolint:errcheck // a map of strings encodes
	if err := db.Put([]byte("_https://app.slack.com\x00\x01localConfig_v2"), append([]byte{0x01}, cfg...), nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	cookies, err := sql.Open("sqlite", filepath.Join(dir, "Cookies"))
	if err != nil {
		t.Fatal(err)
	}
	defer cookies.Close()
	for _, q := range []string{
		`CREATE TABLE cookies (host_key TEXT, name TEXT, value TEXT, encrypted_value BLOB)`,
		`INSERT INTO cookies VALUES ('.slack.com', 'd', 'xoxd-fixture', x'')`,
	} {
		if _, err := cookies.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return &localauth.Desktop{Dir: dir}
}

// waitStatus returns the first status that match accepts.
func waitStatus(t *testing.T, c *sdktest.Conn, match func(sdk.Status) bool) sdk.Status {
	t.Helper()
	for {
		m, err := c.WaitNote(sdk.MethodStatus)
		if err != nil {
			t.Fatal(err)
		}
		var st sdk.Status
		if err := json.Unmarshal(m.Params, &st); err != nil {
			t.Fatal(err)
		}
		if match(st) {
			return st
		}
	}
}

// waitEvent waits for one event of the team.
func waitEvent(t *testing.T, c *sdktest.Conn, teamID string) {
	t.Helper()
	for {
		m, err := c.WaitNote(sdk.MethodEvent)
		if err != nil {
			t.Fatal(err)
		}
		var ev sdk.Event
		if err := json.Unmarshal(m.Params, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Room != nil && strings.HasPrefix(ev.Room.ID, teamID+"/") {
			return
		}
	}
}

func TestServeSandbox(t *testing.T) {
	fake := startFake(t)
	c := start(t, newAdapter(fake, newStore(t, map[string]config.Workspace{"sandbox": sandboxWS}), nil))
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

func TestEmptyStore(t *testing.T) {
	c := start(t, newAdapter(startFake(t), newStore(t, nil), nil))
	st := waitStatus(t, c, func(sdk.Status) bool { return true })
	if st.State != sdk.StateAuthRequired || st.Reason != sdk.ReasonLoggedOut {
		t.Errorf("status %+v", st)
	}
}

func TestLoginMethods(t *testing.T) {
	fake := startFake(t)
	signedIn := desktopFixture(t, fakeslack.Session())
	cases := []struct {
		name      string
		local     map[string]Local
		available []string
	}{
		{"nothing on this computer", nil, []string{MethodToken, MethodSession}},
		{"slack app", map[string]Local{MethodDesktop: signedIn}, []string{MethodDesktop, MethodToken, MethodSession}},
		{"empty slack app", map[string]Local{MethodDesktop: &localauth.Desktop{Dir: t.TempDir()}}, []string{MethodToken, MethodSession}},
		{"empty slk folder", map[string]Local{MethodSlk: localauth.Slk{Dir: t.TempDir()}}, []string{MethodToken, MethodSession}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			methods, err := newAdapter(fake, newStore(t, nil), tc.local).LoginMethods(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var ids, available []string
			for _, m := range methods {
				ids = append(ids, m.ID)
				if m.Available {
					available = append(available, m.ID)
				} else if m.Reason == "" {
					t.Errorf("method %s has no reason", m.ID)
				}
			}
			if !slices.Equal(ids, []string{MethodDesktop, MethodSlk, MethodToken, MethodSession}) {
				t.Errorf("ids %v", ids)
			}
			if !slices.Equal(available, tc.available) {
				t.Errorf("available %v, want %v", available, tc.available)
			}
		})
	}
}

func TestDesktopLogin(t *testing.T) {
	ocral := localauth.Session{TeamID: ocralID, TeamName: "Ocral", Domain: "ocral", Token: ocralToken}
	cases := []struct {
		name  string
		saved map[string]config.Workspace
		want  []string // preselected team ids
	}{
		{"no saved workspace preselects all", nil, []string{ocralID, fakeslack.TeamID}},
		{"saved workspaces are preselected", map[string]config.Workspace{"ocral": ocralWS}, []string{ocralID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := startFake(t)
			store := newStore(t, tc.saved)
			app := desktopFixture(t, fakeslack.Session(), ocral)
			c := start(t, newAdapter(fake, store, map[string]Local{MethodDesktop: app}))
			var begin sdk.AuthBeginResult
			if err := c.Call(sdk.MethodAuthBegin, sdk.AuthBeginParams{Method: MethodDesktop}, &begin); err != nil {
				t.Fatal(err)
			}
			if len(begin.Step.Fields) != 1 {
				t.Fatalf("fields %+v", begin.Step.Fields)
			}
			f := begin.Step.Fields[0]
			if f.Label != "Workspaces" || !f.Multiple || len(f.Choices) != 2 || f.Choices[0].Label != "Ocral" || f.Choices[0].Description != "ocral.slack.com" {
				t.Errorf("field %+v", f)
			}
			if !slices.Equal(f.Default, sdk.Value(tc.want)) {
				t.Errorf("default %v, want %v", f.Default, tc.want)
			}
			pick := sdk.Value{ocralID, fakeslack.TeamID}
			if err := c.Call(sdk.MethodAuthSubmit, sdk.AuthSubmitParams{FlowID: begin.FlowID, Fields: map[string]sdk.Value{"workspaces": pick}}, nil); err != nil {
				t.Fatal(err)
			}
			done, err := c.WaitNote(sdk.MethodAuthDone)
			if err != nil {
				t.Fatal(err)
			}
			var d sdk.AuthDoneParams
			if err := json.Unmarshal(done.Params, &d); err != nil {
				t.Fatal(err)
			}
			if !d.OK || d.Account == nil || d.Account.Name != "Ocral, Sandbox" {
				t.Errorf("done %+v", d)
			}
			ws := store.Workspaces()
			if len(ws) != 2 || ws["sandbox"].Cookie != "xoxd-fixture" || ws["sandbox"].Source != config.SourceDesktop || ws["ocral"].Token != ocralToken {
				t.Errorf("store %+v", ws)
			}
			waitEvent(t, c, fakeslack.TeamID) // the new workspace streams with no restart
		})
	}
}

func TestPartialReject(t *testing.T) {
	fake := startFake(t)
	fake.Reject(ocralToken)
	c := start(t, newAdapter(fake, newStore(t, map[string]config.Workspace{"sandbox": sandboxWS, "ocral": ocralWS}), nil))
	st := waitStatus(t, c, func(s sdk.Status) bool { return s.Message != "" })
	if st.State != sdk.StateAuthRequired || st.Message != "Slack rejected the login for Ocral. Log in again." {
		t.Errorf("status %+v", st)
	}
	got := map[string]string{}
	for _, a := range st.Accounts {
		got[a.ID] = a.State
	}
	if got[ocralID] != sdk.StateAuthRequired || got[fakeslack.TeamID] != sdk.StateConnected {
		t.Errorf("accounts %+v", st.Accounts)
	}
	waitEvent(t, c, fakeslack.TeamID) // the good workspace keeps running
}

func TestKeyLogin(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		fields  map[string]sdk.Value
		invalid bool
		message string // of the new account
	}{
		{"token", MethodToken, map[string]sdk.Value{"token": {"xoxp-1"}}, false, liveNote},
		{"session as token", MethodToken, map[string]sdk.Value{"token": {"xoxc-1"}}, true, ""},
		{"session", MethodSession, map[string]sdk.Value{"token": {"xoxc-1"}, "cookie": {"xoxd-1"}}, false, ""},
		{"session with no cookie prefix", MethodSession, map[string]sdk.Value{"token": {"xoxc-1"}, "cookie": {"abc"}}, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := start(t, newAdapter(startFake(t), newStore(t, nil), nil))
			var begin sdk.AuthBeginResult
			if err := c.Call(sdk.MethodAuthBegin, sdk.AuthBeginParams{Method: tc.method}, &begin); err != nil {
				t.Fatal(err)
			}
			err := c.Call(sdk.MethodAuthSubmit, sdk.AuthSubmitParams{FlowID: begin.FlowID, Fields: tc.fields}, nil)
			if tc.invalid {
				if sdk.CodeOf(err) != sdk.CodeInvalid {
					t.Errorf("submit: %v, want invalid", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			st := waitStatus(t, c, func(s sdk.Status) bool { return s.State == sdk.StateConnected })
			if len(st.Accounts) != 1 || st.Accounts[0].ID != fakeslack.TeamID || st.Accounts[0].Message != tc.message {
				t.Errorf("accounts %+v", st.Accounts)
			}
		})
	}
}

func TestLogout(t *testing.T) {
	store := newStore(t, map[string]config.Workspace{"sandbox": sandboxWS, "ocral": ocralWS})
	c := start(t, newAdapter(startFake(t), store, nil))
	if err := c.Call(sdk.MethodAuthLogout, sdk.Empty{}, nil); err != nil {
		t.Fatal(err)
	}
	if names := store.Names(); len(names) != 0 {
		t.Errorf("store keeps %v", names)
	}
	var st sdk.AuthStatusResult
	if err := c.Call(sdk.MethodAuthStatus, sdk.Empty{}, &st); err != nil {
		t.Fatal(err)
	}
	if st.State != sdk.StateAuthRequired || len(st.Accounts) != 0 {
		t.Errorf("auth/status %+v", st)
	}
}

func TestOps(t *testing.T) {
	fake := startFake(t)
	cl := client.New(sandboxWS)
	cl.SetBaseURL(fake.APIURL())
	team := NewTeam("sandbox", sandboxWS, cl, client.NewDirectory(cl, "", "sandbox"))
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
