package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type recorded struct {
	Method, Path, Query, APIKey string
	Body                        map[string]any
}

// fakePlane records each request and answers with canned bodies by path.
func fakePlane(t *testing.T, routes map[string]string) *[]recorded {
	t.Helper()
	var mu sync.Mutex
	reqs := &[]recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recorded{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, APIKey: r.Header.Get("X-Api-Key")}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &rec.Body)
		}
		mu.Lock()
		*reqs = append(*reqs, rec)
		mu.Unlock()
		if body, ok := routes[r.Method+" "+r.URL.Path]; ok {
			fmt.Fprint(w, body)
			return
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"type":"not_found","code":"not_found","detail":"no route"}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("PLANE_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	t.Setenv("PLANE_HOST", srv.URL)
	t.Setenv("PLANE_API_KEY", "test-key")
	t.Setenv("PLANE_WORKSPACE", "acme")
	t.Setenv("PLANE_PROJECT", "")
	return reqs
}

func runCLI(t *testing.T, args ...string) error {
	t.Helper()
	return run(context.Background(), append([]string{"plane-cli", "-o", "json"}, args...))
}

const projectUUID = "11111111-1111-1111-1111-111111111111"
const itemUUID = "22222222-2222-2222-2222-222222222222"

func TestWorkItemCreateUsesV2AndTypedFlags(t *testing.T) {
	reqs := fakePlane(t, map[string]string{
		"POST /api/v2/workspaces/acme/projects/ENG/work-items/": `{"id":"x","name":"Bug"}`,
	})
	err := runCLI(t, "-p", "ENG", "wi", "create", "--name", "Bug", "--labels", "a,b", "--priority", "high", "--set", "estimate=5")
	if err != nil {
		t.Fatal(err)
	}
	got := (*reqs)[0]
	if got.Body["name"] != "Bug" || got.Body["priority"] != "high" || got.Body["estimate"] != float64(5) {
		t.Errorf("body = %v", got.Body)
	}
	if labels, _ := got.Body["labels"].([]any); len(labels) != 2 {
		t.Errorf("labels = %v", got.Body["labels"])
	}
}

func TestWorkItemCommentTakesProjectFromKey(t *testing.T) {
	reqs := fakePlane(t, map[string]string{
		"GET /api/v2/workspaces/acme/work-items/ENG-7/":                       `{"id":"x"}`,
		"GET /api/v2/workspaces/acme/projects/ENG/work-items/ENG-7/comments/": `{"data":[],"next":null,"pagination":{"style":"offset"}}`,
	})
	if err := runCLI(t, "wi", "get", "ENG-7"); err != nil {
		t.Fatal(err)
	}
	if err := runCLI(t, "wi", "comment", "list", "ENG-7"); err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 2 {
		t.Fatalf("requests = %v", *reqs)
	}
}

func TestMissingRequiredField(t *testing.T) {
	fakePlane(t, nil)
	err := runCLI(t, "-p", "ENG", "wi", "create")
	if err == nil || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingProject(t *testing.T) {
	fakePlane(t, nil)
	err := runCLI(t, "state", "list")
	if err == nil || !strings.Contains(err.Error(), "no project set") {
		t.Fatalf("err = %v", err)
	}
}

func TestDualScopeSelection(t *testing.T) {
	reqs := fakePlane(t, map[string]string{
		"GET /api/v2/workspaces/acme/pages/":              `{"data":[],"next":null,"pagination":{"style":"offset"}}`,
		"GET /api/v2/workspaces/acme/projects/ENG/pages/": `{"data":[],"next":null,"pagination":{"style":"offset"}}`,
	})
	for _, args := range [][]string{{"page", "list"}, {"-p", "ENG", "page", "list"}, {"-p", "ENG", "page", "list", "-W"}, {"-W", "-p", "ENG", "page", "list"}} {
		if err := runCLI(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	want := []string{"/api/v2/workspaces/acme/pages/", "/api/v2/workspaces/acme/projects/ENG/pages/", "/api/v2/workspaces/acme/pages/", "/api/v2/workspaces/acme/pages/"}
	for i, w := range want {
		if (*reqs)[i].Path != w {
			t.Errorf("call %d: %s, want %s", i, (*reqs)[i].Path, w)
		}
	}
}

func TestV1ResolvesIdentifiers(t *testing.T) {
	reqs := fakePlane(t, map[string]string{
		"GET /api/v2/workspaces/acme/projects/ENG/":                                                       `{"id":"` + projectUUID + `"}`,
		"GET /api/v2/workspaces/acme/work-items/ENG-7/":                                                   `{"id":"` + itemUUID + `"}`,
		"GET /api/v1/workspaces/acme/projects/" + projectUUID + "/work-items/" + itemUUID + "/relations/": `{"blocking":[]}`,
	})
	if err := runCLI(t, "wi", "relation", "list", "ENG-7"); err != nil {
		t.Fatal(err)
	}
	last := (*reqs)[len(*reqs)-1]
	if !strings.HasPrefix(last.Path, "/api/v1/") || !strings.Contains(last.Path, itemUUID) {
		t.Errorf("last call = %s", last.Path)
	}
}

func TestListAllFollowsCursor(t *testing.T) {
	reqs := fakePlane(t, map[string]string{
		"GET /api/v2/workspaces/acme/projects/": `{"data":[{"id":"1"}],"next_cursor":null,"has_more":false,"pagination":{"style":"cursor"}}`,
	})
	if err := runCLI(t, "project", "list", "--all"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*reqs)[0].Query, "paginate=cursor") {
		t.Errorf("query = %s", (*reqs)[0].Query)
	}
}

func TestAPIPassthrough(t *testing.T) {
	reqs := fakePlane(t, map[string]string{
		"PATCH /api/v2/workspaces/acme/projects/ENG/": `{"id":"x"}`,
	})
	if err := runCLI(t, "-p", "ENG", "api", "PATCH", "workspaces/{workspace}/projects/{project}/", "-d", `{"name":"n"}`); err != nil {
		t.Fatal(err)
	}
	if (*reqs)[0].Body["name"] != "n" {
		t.Errorf("body = %v", (*reqs)[0].Body)
	}
}

func TestFilterKeepsCommas(t *testing.T) {
	reqs := fakePlane(t, map[string]string{
		"GET /api/v2/workspaces/acme/work-items/ENG-7/": `{"id":"x"}`,
	})
	if err := runCLI(t, "wi", "get", "ENG-7", "-q", "expand=state,labels"); err != nil {
		t.Fatal(err)
	}
	if (*reqs)[0].Query != "expand=state%2Clabels" {
		t.Errorf("query = %s", (*reqs)[0].Query)
	}
}

func TestBodyKeysResolveToUUIDs(t *testing.T) {
	reqs := fakePlane(t, map[string]string{
		"GET /api/v2/workspaces/acme/work-items/ENG-7/":                   `{"id":"` + itemUUID + `"}`,
		"POST /api/v2/workspaces/acme/projects/ENG/cycles/c1/work-items/": `{"added":[]}`,
	})
	if err := runCLI(t, "-p", "ENG", "cycle", "manage-work-items", "c1", "--add", "ENG-7,"+projectUUID); err != nil {
		t.Fatal(err)
	}
	add, _ := (*reqs)[len(*reqs)-1].Body["add"].([]any)
	if len(add) != 2 || add[0] != itemUUID || add[1] != projectUUID {
		t.Errorf("add = %v", add)
	}
}

func TestUploadSendsBareMIMEType(t *testing.T) {
	reqs := fakePlane(t, nil)
	f := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(f, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = runCLI(t, "asset", "upload", f)
	if len(*reqs) == 0 || (*reqs)[0].Body["type"] != "text/plain" {
		t.Errorf("requests = %v", *reqs)
	}
}

func TestViewFiltersSentAsJSON(t *testing.T) {
	reqs := fakePlane(t, map[string]string{
		"POST /api/v2/workspaces/acme/views/": `{"id":"v"}`,
	})
	if err := runCLI(t, "view", "create", "--name", "v", "--filters", `{"priority":["high"]}`, "--description", "[x]"); err != nil {
		t.Fatal(err)
	}
	b := (*reqs)[0].Body
	if _, ok := b["filters"].(map[string]any); !ok || b["description"] != "[x]" {
		t.Errorf("body = %v", b)
	}
}

func TestViewJSONFieldFromFile(t *testing.T) {
	reqs := fakePlane(t, map[string]string{
		"PATCH /api/v2/workspaces/acme/views/v/": `{"id":"v"}`,
	})
	f := filepath.Join(t.TempDir(), "dp.json")
	if err := os.WriteFile(f, []byte(`{"key":true,"cycle":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runCLI(t, "view", "update", "v", "--display-properties", "@"+f); err != nil {
		t.Fatal(err)
	}
	if dp, ok := (*reqs)[0].Body["display_properties"].(map[string]any); !ok || dp["cycle"] != false {
		t.Errorf("body = %v", (*reqs)[0].Body)
	}
	if err := runCLI(t, "view", "update", "v", "--display-properties", "{bad"); err == nil || len(*reqs) != 1 {
		t.Errorf("bad JSON: err=%v, requests=%d", err, len(*reqs))
	}
}

func TestMarkdownFlag(t *testing.T) {
	reqs := fakePlane(t, map[string]string{
		"POST /api/v2/workspaces/acme/projects/ENG/work-items/ENG-7/comments/": `{"id":"c"}`,
	})
	if err := runCLI(t, "wi", "comment", "create", "ENG-7", "--comment-md", "**hi** ~~x~~"); err != nil {
		t.Fatal(err)
	}
	if got := (*reqs)[0].Body["comment_html"]; got != "<p><strong>hi</strong> <del>x</del></p>" {
		t.Errorf("comment_html = %q", got)
	}
}

func TestMarkdownFlagRejectsBadInput(t *testing.T) {
	reqs := fakePlane(t, nil)
	err := runCLI(t, "wi", "comment", "create", "ENG-7", "--comment-md", "text\n```sh\nrun")
	if err == nil || !strings.Contains(err.Error(), "--comment-md: markdown: line 2: code fence") {
		t.Fatalf("err = %v", err)
	}
	err = runCLI(t, "wi", "comment", "create", "ENG-7", "--comment-md", "x", "--comment-html", "<p>x</p>")
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("err = %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("sent %d requests", len(*reqs))
	}
}

func TestWorkspaceKeySelection(t *testing.T) {
	reqs := fakePlane(t, map[string]string{
		"GET /api/v2/workspaces/acme/projects/":  `{"data":[],"next":null,"pagination":{"style":"offset"}}`,
		"GET /api/v2/workspaces/other/projects/": `{"data":[],"next":null,"pagination":{"style":"offset"}}`,
	})
	t.Setenv("PLANE_API_KEY", "")
	if err := runCLI(t, "auth", "workspace", "add", "other", "--api-key", "other-key"); err != nil {
		t.Fatal(err)
	}
	if err := runCLI(t, "config", "set", "api_key", "default-key"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"project", "list"}, {"-w", "other", "project", "list"}, {"-w", "other", "-k", "flag-key", "project", "list"}} {
		if err := runCLI(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	got := []string{}
	for _, r := range *reqs {
		got = append(got, r.APIKey)
	}
	// The first request is the check in "auth workspace add".
	want := []string{"other-key", "default-key", "other-key", "flag-key"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("keys = %v, want %v", got, want)
	}
}
