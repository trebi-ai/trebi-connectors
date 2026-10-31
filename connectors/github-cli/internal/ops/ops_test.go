package ops

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/fakegithub"
)

func newOps(t *testing.T) (*Ops, *fakegithub.Server) {
	t.Helper()
	fake := fakegithub.Start()
	t.Cleanup(fake.Close)
	cl := client.New(fakegithub.Token)
	cl.API = fake.URL()
	return New(cl), fake
}

// run runs one operation with a JSON input and returns the answer as JSON.
func run(t *testing.T, o *Ops, name, input string) (map[string]any, error) {
	t.Helper()
	out, err := o.Run(context.Background(), name, []byte(input))
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m, nil
}

// wantErr checks the exit code, the message, and the GitHub status.
func wantErr(t *testing.T, err error, code int, msg string, status int) {
	t.Helper()
	var oe *Error
	if !errors.As(err, &oe) || oe.Code != code || !strings.Contains(oe.Message, msg) || oe.Status != status {
		t.Fatalf("want code %d %q status %d, got %#v", code, msg, status, err)
	}
	var line map[string]any
	if json.Unmarshal([]byte(oe.JSON()), &line) != nil || line["error"] != oe.Message {
		t.Fatalf("JSON line %s", oe.JSON())
	}
}

func TestSearch(t *testing.T) {
	t.Parallel()
	o, _ := newOps(t)
	m, err := run(t, o, "search", `{"query":"repo:octo/app is:pr","limit":1}`)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := m["items"].([]any)
	if m["total"] != float64(1) || len(items) != 1 {
		t.Fatalf("search %v", m)
	}
	it, _ := items[0].(map[string]any)
	if it["repository"] != "octo/app" || it["number"] != float64(8) || it["is_pull_request"] != true || it["author"] != "hubot" {
		t.Fatalf("item %v", it)
	}
	_, err = run(t, o, "search", `{}`)
	wantErr(t, err, ExitInput, "query is required", 0)
	_, err = run(t, o, "search", `{"query":"x","limit":51}`)
	wantErr(t, err, ExitInput, "limit must be from 1 to 50", 0)
	_, err = run(t, o, "search", `{"query":"is:invalid"}`)
	wantErr(t, err, ExitAPI, "Validation Failed", 422)
	_, err = run(t, o, "search", `{"query":"x","extra":1}`)
	wantErr(t, err, ExitInput, "unknown field", 0)
}

func TestGet(t *testing.T) {
	t.Parallel()
	o, _ := newOps(t)
	m, err := run(t, o, "get", `{"repository":"octo/app","number":8}`)
	if err != nil {
		t.Fatal(err)
	}
	if m["is_pull_request"] != true || m["base"] != "main" || m["head"] != "feature-8" || m["mergeable_state"] != "clean" {
		t.Fatalf("pull request %v", m)
	}
	reviews, _ := m["reviews"].([]any)
	if len(reviews) != 1 || reviews[0].(map[string]any)["state"] != "approved" { //nolint:forcetypeassert // the test fails on a bad shape
		t.Fatalf("the last review of each reviewer, no comments: %v", reviews)
	}
	checks, _ := m["checks"].(map[string]any)
	if checks["passed"] != float64(1) || checks["failed"] != float64(1) || checks["pending"] != float64(1) {
		t.Fatalf("checks %v", checks)
	}
	comments, _ := m["comments"].([]any)
	last, _ := comments[len(comments)-1].(map[string]any)
	if len(comments) != 2 || last["truncated"] != true || len(last["body"].(string)) != maxBody { //nolint:forcetypeassert // the test fails on a bad shape
		t.Fatalf("comments %v", comments)
	}
	m, err = run(t, o, "get", `{"repository":"octo/app","number":7,"comments":0}`)
	if err != nil || m["is_pull_request"] != false || m["reviews"] != nil || len(m["comments"].([]any)) != 0 { //nolint:forcetypeassert // the test fails on a bad shape
		t.Fatalf("issue %v %v", m, err)
	}
	_, err = run(t, o, "get", `{"repository":"octo/app"}`)
	wantErr(t, err, ExitInput, "number is required", 0)
	_, err = run(t, o, "get", `{"repository":"octo","number":1}`)
	wantErr(t, err, ExitInput, "owner/name", 0)
	_, err = run(t, o, "get", `{"repository":"octo/app","number":99}`)
	wantErr(t, err, ExitAPI, "Not Found", 404)
}

func TestMyWork(t *testing.T) {
	t.Parallel()
	o, _ := newOps(t)
	m, err := run(t, o, "my_work", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	rr, _ := m["review_requested"].([]any)
	as, _ := m["assigned"].([]any)
	mine, _ := m["my_pull_requests"].([]any)
	if len(rr) != 1 || len(as) != 1 || len(mine) != 1 {
		t.Fatalf("my work %v", m)
	}
	pr, _ := mine[0].(map[string]any)
	if pr["number"] != float64(9) || pr["review_decision"] != "approved" || pr["checks"] == nil {
		t.Fatalf("own pull request %v", pr)
	}
	_, err = run(t, o, "my_work", `{"limit":-1}`)
	wantErr(t, err, ExitInput, "limit", 0)
}

func TestMyWorkEmpty(t *testing.T) {
	t.Parallel()
	o, _ := newOps(t)
	o.gh.Token = fakegithub.Idle
	out, err := o.Run(context.Background(), "my_work", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := json.Marshal(out); string(data) != `{"review_requested":[],"assigned":[],"my_pull_requests":[]}` { //nolint:errchkjson // plain values
		t.Fatalf("empty groups must be [] lists: %s", data)
	}
}

func TestComment(t *testing.T) {
	t.Parallel()
	o, fake := newOps(t)
	m, err := run(t, o, "comment", `{"repository":"octo/app","number":7,"body":"Done."}`)
	if err != nil || !strings.Contains(m["url"].(string), "issuecomment") || m["id"] == nil { //nolint:forcetypeassert // the test fails on a bad shape
		t.Fatalf("comment %v %v", m, err)
	}
	if !slices.Contains(fake.Calls(), "POST /repos/octo/app/issues/7/comments") {
		t.Fatalf("calls %v", fake.Calls())
	}
	_, err = run(t, o, "comment", `{"repository":"octo/app","number":7}`)
	wantErr(t, err, ExitInput, "body is required", 0)
	_, err = run(t, o, "comment", `{"repository":"octo/app","number":99,"body":"x"}`)
	wantErr(t, err, ExitAPI, "Not Found", 404)
}

func TestCreateIssue(t *testing.T) {
	t.Parallel()
	o, _ := newOps(t)
	m, err := run(t, o, "create_issue", `{"repository":"octo/app","title":"Flaky test","labels":["bug"],"assignees":["octo"]}`)
	if err != nil || m["number"] != float64(10) {
		t.Fatalf("create %v %v", m, err)
	}
	_, err = run(t, o, "create_issue", `{"repository":"octo/app"}`)
	wantErr(t, err, ExitInput, "title is required", 0)
	_, err = run(t, o, "create_issue", `{"repository":"octo/missing","title":"x"}`)
	wantErr(t, err, ExitAPI, "Not Found", 404)
}

func TestReviewPR(t *testing.T) {
	t.Parallel()
	o, _ := newOps(t)
	if m, err := run(t, o, "review_pr", `{"repository":"octo/app","number":8,"verdict":"approve"}`); err != nil || m["url"] == "" {
		t.Fatalf("approve %v %v", m, err)
	}
	if _, err := run(t, o, "review_pr", `{"repository":"octo/app","number":8,"verdict":"request_changes","body":"Add a test."}`); err != nil {
		t.Fatal(err)
	}
	_, err := run(t, o, "review_pr", `{"repository":"octo/app","number":8,"verdict":"request_changes"}`)
	wantErr(t, err, ExitInput, "body is required for request_changes", 0)
	_, err = run(t, o, "review_pr", `{"repository":"octo/app","number":8,"verdict":"lgtm"}`)
	wantErr(t, err, ExitInput, "verdict must be", 0)
	_, err = run(t, o, "review_pr", `{"repository":"octo/app","number":9,"verdict":"approve"}`)
	wantErr(t, err, ExitAPI, "Can not approve your own pull request", 422)
}

func TestMergePR(t *testing.T) {
	t.Parallel()
	o, fake := newOps(t)
	m, err := run(t, o, "merge_pr", `{"repository":"octo/app","number":8,"delete_branch":true}`)
	if err != nil || m["merged"] != true || m["sha"] != "merged1" || m["branch_deleted"] != true {
		t.Fatalf("merge %v %v", m, err)
	}
	if !slices.Contains(fake.Calls(), "DELETE /repos/octo/app/git/refs/heads/feature-8") {
		t.Fatalf("calls %v", fake.Calls())
	}
	_, err = run(t, o, "merge_pr", `{"repository":"octo/app","number":8,"method":"fast"}`)
	wantErr(t, err, ExitInput, "method must be", 0)
	_, err = run(t, o, "merge_pr", `{"repository":"octo/app","number":9}`)
	wantErr(t, err, ExitAPI, "Pull Request is not mergeable", 405)
}

func TestRerunWorkflow(t *testing.T) {
	t.Parallel()
	o, fake := newOps(t)
	if _, err := run(t, o, "rerun_workflow", `{"repository":"octo/app","run_id":1}`); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, o, "rerun_workflow", `{"repository":"octo/app","run_id":1,"failed_only":false}`); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if !slices.Contains(calls, "POST /repos/octo/app/actions/runs/1/rerun-failed-jobs") || !slices.Contains(calls, "POST /repos/octo/app/actions/runs/1/rerun") {
		t.Fatalf("calls %v", calls)
	}
	_, err := run(t, o, "rerun_workflow", `{"repository":"octo/app"}`)
	wantErr(t, err, ExitInput, "run_id is required", 0)
	_, err = run(t, o, "rerun_workflow", `{"repository":"octo/app","run_id":77}`)
	wantErr(t, err, ExitAPI, "Not Found", 404)
}

func TestUnknownOperation(t *testing.T) {
	t.Parallel()
	o, _ := newOps(t)
	_, err := run(t, o, "delete_repo", `{}`)
	wantErr(t, err, ExitInput, "unknown operation", 0)
}
