// Package ops holds the operations of the Trebi connection: three that
// only read, and five that change data. Each takes one JSON object and
// returns one JSON object. The manifest declares the same names and
// inputs.
package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/client"
)

// Exit codes of a failed operation.
const (
	ExitAPI   = 1 // GitHub refused the call
	ExitInput = 2 // the input is not valid
)

// maxBody is the longest body text in an answer.
const maxBody = 8 << 10

// Error is a failed operation. Status is the GitHub HTTP status, or 0.
type Error struct {
	Code    int    `json:"-"`
	Message string `json:"error"`
	Status  int    `json:"status,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// JSON is the error as one JSON line for stderr.
func (e *Error) JSON() string {
	b, _ := json.Marshal(e) //nolint:errcheck // a struct of plain values
	return string(b)
}

func invalid(format string, args ...any) *Error {
	return &Error{Code: ExitInput, Message: fmt.Sprintf(format, args...)}
}

// apiErr turns a GitHub error into an operation error.
func apiErr(what string, err error) error {
	if err == nil {
		return nil
	}
	var ae *client.APIError
	if errors.As(err, &ae) {
		return &Error{Code: ExitAPI, Message: what + ": " + ae.Message, Status: ae.Status}
	}
	return &Error{Code: ExitAPI, Message: what + ": " + err.Error()}
}

// Ops runs the operations with one GitHub client.
type Ops struct{ gh *client.Client }

// New returns the operations of a client.
func New(gh *client.Client) *Ops { return &Ops{gh: gh} }

// Names lists the operations in the order of the manifest.
func Names() []string {
	return []string{"search", "get", "my_work", "comment", "create_issue", "review_pr", "merge_pr", "rerun_workflow"}
}

// Run decodes the input of one operation, runs it, and returns the answer.
func (o *Ops) Run(ctx context.Context, name string, input []byte) (any, error) {
	if len(bytes.TrimSpace(input)) == 0 {
		input = []byte("{}")
	}
	dec := func(v any) error {
		d := json.NewDecoder(bytes.NewReader(input))
		d.DisallowUnknownFields()
		if err := d.Decode(v); err != nil {
			return invalid("the input is not valid: %v", err)
		}
		return nil
	}
	switch name {
	case "search":
		var in SearchInput
		if err := dec(&in); err != nil {
			return nil, err
		}
		return o.Search(ctx, in)
	case "get":
		var in GetInput
		if err := dec(&in); err != nil {
			return nil, err
		}
		return o.Get(ctx, in)
	case "my_work":
		var in MyWorkInput
		if err := dec(&in); err != nil {
			return nil, err
		}
		return o.MyWork(ctx, in)
	case "comment":
		var in CommentInput
		if err := dec(&in); err != nil {
			return nil, err
		}
		return o.Comment(ctx, in)
	case "create_issue":
		var in CreateIssueInput
		if err := dec(&in); err != nil {
			return nil, err
		}
		return o.CreateIssue(ctx, in)
	case "review_pr":
		var in ReviewInput
		if err := dec(&in); err != nil {
			return nil, err
		}
		return o.ReviewPR(ctx, in)
	case "merge_pr":
		var in MergeInput
		if err := dec(&in); err != nil {
			return nil, err
		}
		return o.MergePR(ctx, in)
	case "rerun_workflow":
		var in RerunInput
		if err := dec(&in); err != nil {
			return nil, err
		}
		return o.RerunWorkflow(ctx, in)
	}
	return nil, invalid("unknown operation %q: use one of %s", name, strings.Join(Names(), ", "))
}

// Item is one issue or pull request in a list.
type Item struct {
	Repository     string   `json:"repository"`
	Number         int      `json:"number"`
	Title          string   `json:"title"`
	State          string   `json:"state"`
	IsPullRequest  bool     `json:"is_pull_request"`
	Author         string   `json:"author"`
	Labels         []string `json:"labels"`
	UpdatedAt      string   `json:"updated_at"`
	URL            string   `json:"url"`
	Checks         *Checks  `json:"checks,omitempty"`
	ReviewDecision *string  `json:"review_decision,omitempty"`
}

// issue is the GitHub shape of an issue or a pull request in the issues
// and search APIs.
type issue struct {
	Number        int             `json:"number"`
	Title         string          `json:"title"`
	State         string          `json:"state"`
	Body          string          `json:"body"`
	HTMLURL       string          `json:"html_url"`
	RepositoryURL string          `json:"repository_url"`
	UpdatedAt     string          `json:"updated_at"`
	Comments      int             `json:"comments"`
	PullRequest   json.RawMessage `json:"pull_request"`
	User          client.User     `json:"user"`
	Labels        []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Assignees []client.User `json:"assignees"`
}

func (is issue) item() Item {
	it := Item{
		Repository: repoOf(is.RepositoryURL), Number: is.Number, Title: is.Title, State: is.State,
		IsPullRequest: len(is.PullRequest) > 0 && string(is.PullRequest) != "null",
		Author:        is.User.Login, Labels: []string{}, UpdatedAt: is.UpdatedAt, URL: is.HTMLURL,
	}
	for _, l := range is.Labels {
		it.Labels = append(it.Labels, l.Name)
	}
	return it
}

// repoOf returns owner/name of a repository API URL.
func repoOf(u string) string {
	_, after, ok := strings.Cut(u, "/repos/")
	if !ok {
		return ""
	}
	return after
}

// SearchInput is the input of search.
type SearchInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// SearchResult is the answer of search.
type SearchResult struct {
	Total int    `json:"total"`
	Items []Item `json:"items"`
}

// Search finds issues and pull requests with the GitHub search words.
func (o *Ops) Search(ctx context.Context, in SearchInput) (SearchResult, error) {
	if strings.TrimSpace(in.Query) == "" {
		return SearchResult{}, invalid("query is required")
	}
	limit, err := bound("limit", in.Limit, 1, 50, 20)
	if err != nil {
		return SearchResult{}, err
	}
	return o.search(ctx, in.Query, limit)
}

func (o *Ops) search(ctx context.Context, q string, limit int) (SearchResult, error) {
	var body struct {
		Total int     `json:"total_count"`
		Items []issue `json:"items"`
	}
	v := url.Values{"q": {q}, "per_page": {strconv.Itoa(limit)}, "sort": {"updated"}}
	if err := o.gh.Do(ctx, http.MethodGet, "/search/issues?"+v.Encode(), nil, &body); err != nil {
		return SearchResult{}, apiErr("search", err)
	}
	out := SearchResult{Total: body.Total, Items: []Item{}}
	for _, is := range body.Items {
		out.Items = append(out.Items, is.item())
	}
	return out, nil
}

// GetInput is the input of get.
type GetInput struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Comments   *int   `json:"comments"`
}

// Comment is one comment of an issue or a pull request.
type Comment struct {
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
	Body      string `json:"body"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Review is the last review of one reviewer.
type Review struct {
	Author string `json:"author"`
	State  string `json:"state"`
}

// Checks counts the check runs of the head commit of a pull request.
type Checks struct {
	Passed  int      `json:"passed"`
	Failed  int      `json:"failed"`
	Pending int      `json:"pending"`
	Names   []string `json:"failed_names"`
}

// Detail is the answer of get.
type Detail struct {
	Item
	Body      string    `json:"body"`
	Truncated bool      `json:"truncated,omitempty"`
	Assignees []string  `json:"assignees"`
	Comments  []Comment `json:"comments"`
	// Pull request fields.
	Draft          *bool    `json:"draft,omitempty"`
	Merged         *bool    `json:"merged,omitempty"`
	MergeableState string   `json:"mergeable_state,omitempty"`
	Base           string   `json:"base,omitempty"`
	Head           string   `json:"head,omitempty"`
	Reviews        []Review `json:"reviews,omitempty"`
}

// pull is the GitHub shape of a pull request in the pulls API.
type pull struct {
	Draft          bool   `json:"draft"`
	Merged         bool   `json:"merged"`
	MergeableState string `json:"mergeable_state"`
	Base           struct {
		Ref  string `json:"ref"`
		Repo struct {
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
		} `json:"repo"`
	} `json:"base"`
	Head struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
}

// Get shows one issue or pull request with its last comments. A pull
// request also has its reviews and checks.
func (o *Ops) Get(ctx context.Context, in GetInput) (Detail, error) {
	if err := needRepo(in.Repository, in.Number); err != nil {
		return Detail{}, err
	}
	n := 10
	if in.Comments != nil {
		n = *in.Comments
	}
	if n < 0 || n > 50 {
		return Detail{}, invalid("comments must be from 0 to 50")
	}
	base := fmt.Sprintf("/repos/%s/issues/%d", in.Repository, in.Number)
	var is issue
	if err := o.gh.Do(ctx, http.MethodGet, base, nil, &is); err != nil {
		return Detail{}, apiErr("get", err)
	}
	d := Detail{Item: is.item(), Assignees: []string{}, Comments: []Comment{}}
	if d.Repository == "" {
		d.Repository = in.Repository
	}
	d.Body, d.Truncated = cut(is.Body)
	for _, u := range is.Assignees {
		d.Assignees = append(d.Assignees, u.Login)
	}
	comments, err := o.lastComments(ctx, base, is.Comments, n)
	if err != nil {
		return Detail{}, err
	}
	d.Comments = comments
	if !d.IsPullRequest {
		return d, nil
	}
	pr, err := o.pull(ctx, in.Repository, in.Number)
	if err != nil {
		return Detail{}, err
	}
	d.Draft, d.Merged, d.MergeableState, d.Base, d.Head = &pr.Draft, &pr.Merged, pr.MergeableState, pr.Base.Ref, pr.Head.Ref
	if d.Reviews, err = o.reviews(ctx, in.Repository, in.Number); err != nil {
		return Detail{}, err
	}
	if d.Checks, err = o.checks(ctx, in.Repository, pr.Head.SHA); err != nil {
		return Detail{}, err
	}
	return d, nil
}

func (o *Ops) pull(ctx context.Context, repo string, number int) (pull, error) {
	var pr pull
	err := o.gh.Do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", repo, number), nil, &pr)
	return pr, apiErr("read the pull request", err)
}

// lastComments reads the last n of total comments. The API lists the
// oldest first.
func (o *Ops) lastComments(ctx context.Context, base string, total, n int) ([]Comment, error) {
	out := []Comment{}
	if n == 0 || total == 0 {
		return out, nil
	}
	read := func(page int) ([]Comment, error) {
		var raw []struct {
			User      client.User `json:"user"`
			CreatedAt string      `json:"created_at"`
			Body      string      `json:"body"`
		}
		if err := o.gh.Do(ctx, http.MethodGet, fmt.Sprintf("%s/comments?per_page=%d&page=%d", base, n, page), nil, &raw); err != nil {
			return nil, apiErr("read the comments", err)
		}
		var cs []Comment
		for _, c := range raw {
			body, cut := cut(c.Body)
			cs = append(cs, Comment{Author: c.User.Login, CreatedAt: c.CreatedAt, Body: body, Truncated: cut})
		}
		return cs, nil
	}
	last := (total + n - 1) / n
	cs, err := read(last)
	if err != nil {
		return nil, err
	}
	if len(cs) < n && last > 1 {
		prev, err := read(last - 1)
		if err != nil {
			return nil, err
		}
		cs = append(prev[max(0, len(prev)-(n-len(cs))):], cs...)
	}
	return append(out, cs...), nil
}

// reviews returns the last review of each reviewer, without plain
// comments.
func (o *Ops) reviews(ctx context.Context, repo string, number int) ([]Review, error) {
	var raw []struct {
		User  client.User `json:"user"`
		State string      `json:"state"`
	}
	if err := o.gh.Do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", repo, number), nil, &raw); err != nil {
		return nil, apiErr("read the reviews", err)
	}
	out := []Review{}
	for _, r := range raw {
		if r.State == "COMMENTED" || r.State == "PENDING" {
			continue
		}
		state := strings.ToLower(r.State)
		if i := slices.IndexFunc(out, func(x Review) bool { return x.Author == r.User.Login }); i >= 0 {
			out[i].State = state
			continue
		}
		out = append(out, Review{Author: r.User.Login, State: state})
	}
	return out, nil
}

// decision is the review result of a pull request: changes_requested,
// approved, or review_required.
func decision(reviews []Review) string {
	if slices.ContainsFunc(reviews, func(r Review) bool { return r.State == "changes_requested" }) {
		return "changes_requested"
	}
	if slices.ContainsFunc(reviews, func(r Review) bool { return r.State == "approved" }) {
		return "approved"
	}
	return "review_required"
}

func (o *Ops) checks(ctx context.Context, repo, sha string) (*Checks, error) {
	c := &Checks{Names: []string{}}
	if sha == "" {
		return c, nil
	}
	var body struct {
		Runs []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := o.gh.Do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100", repo, sha), nil, &body); err != nil {
		return nil, apiErr("read the checks", err)
	}
	for _, r := range body.Runs {
		switch {
		case r.Status != "completed":
			c.Pending++
		case slices.Contains([]string{"success", "neutral", "skipped"}, r.Conclusion):
			c.Passed++
		default:
			c.Failed++
			c.Names = append(c.Names, r.Name)
		}
	}
	return c, nil
}

// MyWorkInput is the input of my_work.
type MyWorkInput struct {
	Limit int `json:"limit"`
}

// MyWork is the answer of my_work.
type MyWork struct {
	ReviewRequested []Item `json:"review_requested"`
	Assigned        []Item `json:"assigned"`
	MyPullRequests  []Item `json:"my_pull_requests"`
}

// MyWork lists what needs the account: review requests, assigned issues
// and pull requests, and its own open pull requests with checks and
// review state.
func (o *Ops) MyWork(ctx context.Context, in MyWorkInput) (MyWork, error) {
	limit, err := bound("limit", in.Limit, 1, 50, 20)
	if err != nil {
		return MyWork{}, err
	}
	var out MyWork
	for _, g := range []struct {
		q   string
		dst *[]Item
	}{
		{"is:open review-requested:@me", &out.ReviewRequested},
		{"is:open assignee:@me", &out.Assigned},
		{"is:open is:pr author:@me", &out.MyPullRequests},
	} {
		res, err := o.search(ctx, g.q, limit)
		if err != nil {
			return MyWork{}, err
		}
		*g.dst = res.Items
	}
	errs := make([]error, len(out.MyPullRequests))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)
	for i := range out.MyPullRequests {
		wg.Add(1)
		go func() { // ends after three calls
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			it := &out.MyPullRequests[i]
			pr, err := o.pull(ctx, it.Repository, it.Number)
			if err != nil {
				errs[i] = err
				return
			}
			reviews, err := o.reviews(ctx, it.Repository, it.Number)
			if err != nil {
				errs[i] = err
				return
			}
			d := decision(reviews)
			it.ReviewDecision = &d
			it.Checks, errs[i] = o.checks(ctx, it.Repository, pr.Head.SHA)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return MyWork{}, err
	}
	return out, nil
}

// Created is the answer of an operation that makes a thing.
type Created struct {
	URL    string `json:"url"`
	ID     int64  `json:"id"`
	Number int    `json:"number,omitempty"`
}

type created struct {
	ID      int64  `json:"id"`
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
}

// CommentInput is the input of comment.
type CommentInput struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Body       string `json:"body"`
}

// Comment adds a comment to an issue or a pull request.
func (o *Ops) Comment(ctx context.Context, in CommentInput) (Created, error) {
	if err := needRepo(in.Repository, in.Number); err != nil {
		return Created{}, err
	}
	if strings.TrimSpace(in.Body) == "" {
		return Created{}, invalid("body is required")
	}
	var c created
	err := o.gh.Do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", in.Repository, in.Number), map[string]string{"body": in.Body}, &c)
	return Created{URL: c.HTMLURL, ID: c.ID}, apiErr("comment", err)
}

// CreateIssueInput is the input of create_issue.
type CreateIssueInput struct {
	Repository string   `json:"repository"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Labels     []string `json:"labels"`
	Assignees  []string `json:"assignees"`
}

// CreateIssue opens an issue.
func (o *Ops) CreateIssue(ctx context.Context, in CreateIssueInput) (Created, error) {
	if err := needRepo(in.Repository, 1); err != nil {
		return Created{}, err
	}
	if strings.TrimSpace(in.Title) == "" {
		return Created{}, invalid("title is required")
	}
	body := map[string]any{"title": in.Title}
	if in.Body != "" {
		body["body"] = in.Body
	}
	if len(in.Labels) > 0 {
		body["labels"] = in.Labels
	}
	if len(in.Assignees) > 0 {
		body["assignees"] = in.Assignees
	}
	var c created
	err := o.gh.Do(ctx, http.MethodPost, "/repos/"+in.Repository+"/issues", body, &c)
	return Created{URL: c.HTMLURL, ID: c.ID, Number: c.Number}, apiErr("create the issue", err)
}

// ReviewInput is the input of review_pr.
type ReviewInput struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Verdict    string `json:"verdict"`
	Body       string `json:"body"`
}

// ReviewPR approves a pull request, asks for changes, or comments on it.
func (o *Ops) ReviewPR(ctx context.Context, in ReviewInput) (Created, error) {
	if err := needRepo(in.Repository, in.Number); err != nil {
		return Created{}, err
	}
	event := map[string]string{"approve": "APPROVE", "request_changes": "REQUEST_CHANGES", "comment": "COMMENT"}[in.Verdict]
	if event == "" {
		return Created{}, invalid("verdict must be approve, request_changes, or comment")
	}
	if event != "APPROVE" && strings.TrimSpace(in.Body) == "" {
		return Created{}, invalid("body is required for %s", in.Verdict)
	}
	body := map[string]string{"event": event}
	if in.Body != "" {
		body["body"] = in.Body
	}
	var c created
	err := o.gh.Do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls/%d/reviews", in.Repository, in.Number), body, &c)
	return Created{URL: c.HTMLURL, ID: c.ID}, apiErr("review", err)
}

// MergeInput is the input of merge_pr.
type MergeInput struct {
	Repository   string `json:"repository"`
	Number       int    `json:"number"`
	Method       string `json:"method"`
	DeleteBranch bool   `json:"delete_branch"`
}

// Merged is the answer of merge_pr.
type Merged struct {
	Merged        bool   `json:"merged"`
	SHA           string `json:"sha"`
	BranchDeleted bool   `json:"branch_deleted"`
}

// MergePR merges a pull request. With delete_branch it deletes the head
// branch, but never a branch of a fork or the default branch.
func (o *Ops) MergePR(ctx context.Context, in MergeInput) (Merged, error) {
	if err := needRepo(in.Repository, in.Number); err != nil {
		return Merged{}, err
	}
	method := in.Method
	if method == "" {
		method = "squash"
	}
	if !slices.Contains([]string{"merge", "squash", "rebase"}, method) {
		return Merged{}, invalid("method must be merge, squash, or rebase")
	}
	pr, err := o.pull(ctx, in.Repository, in.Number)
	if err != nil {
		return Merged{}, err
	}
	var res struct {
		SHA    string `json:"sha"`
		Merged bool   `json:"merged"`
	}
	if err := o.gh.Do(ctx, http.MethodPut, fmt.Sprintf("/repos/%s/pulls/%d/merge", in.Repository, in.Number), map[string]string{"merge_method": method}, &res); err != nil {
		return Merged{}, apiErr("merge", err)
	}
	out := Merged{Merged: res.Merged, SHA: res.SHA}
	own := pr.Head.Repo != nil && strings.EqualFold(pr.Head.Repo.FullName, in.Repository)
	if in.DeleteBranch && out.Merged && own && pr.Head.Ref != pr.Base.Repo.DefaultBranch && pr.Head.Ref != "" {
		err := o.gh.Do(ctx, http.MethodDelete, "/repos/"+in.Repository+"/git/refs/heads/"+url.PathEscape(pr.Head.Ref), nil, nil)
		if err != nil {
			return out, apiErr("merged, but delete the branch", err)
		}
		out.BranchDeleted = true
	}
	return out, nil
}

// RerunInput is the input of rerun_workflow.
type RerunInput struct {
	Repository string `json:"repository"`
	RunID      int64  `json:"run_id"`
	FailedOnly *bool  `json:"failed_only"`
}

// RerunWorkflow runs a workflow run again: its failed jobs, or all jobs.
func (o *Ops) RerunWorkflow(ctx context.Context, in RerunInput) (Created, error) {
	if err := needRepo(in.Repository, 1); err != nil {
		return Created{}, err
	}
	if in.RunID <= 0 {
		return Created{}, invalid("run_id is required")
	}
	path := fmt.Sprintf("/repos/%s/actions/runs/%d/rerun", in.Repository, in.RunID)
	if in.FailedOnly == nil || *in.FailedOnly {
		path = fmt.Sprintf("/repos/%s/actions/runs/%d/rerun-failed-jobs", in.Repository, in.RunID)
	}
	if err := o.gh.Do(ctx, http.MethodPost, path, map[string]any{}, nil); err != nil {
		return Created{}, apiErr("run the workflow again", err)
	}
	return Created{URL: fmt.Sprintf("https://github.com/%s/actions/runs/%d", in.Repository, in.RunID), ID: in.RunID}, nil
}

// needRepo checks owner/name and a positive number.
func needRepo(repo string, number int) error {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return invalid("repository is required as owner/name, not %q", repo)
	}
	if number <= 0 {
		return invalid("number is required")
	}
	return nil
}

// bound applies the default to 0 and checks the range.
func bound(name string, v, lo, hi, def int) (int, error) {
	if v == 0 {
		return def, nil
	}
	if v < lo || v > hi {
		return 0, invalid("%s must be from %d to %d", name, lo, hi)
	}
	return v, nil
}

// cut shortens a body to maxBody bytes on a rune border.
func cut(s string) (string, bool) {
	if len(s) <= maxBody {
		return s, false
	}
	i := maxBody
	for i > 0 && !utf8RuneStart(s[i]) {
		i--
	}
	return s[:i], true
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
