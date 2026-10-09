package serve

import (
	"cmp"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// maxRaw bounds the raw payload on an event.
const maxRaw = 64 << 10

// payload holds the fields of the webhook and events API payloads that the
// events use.
type payload struct {
	Action     string `json:"action"`
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Head       string `json:"head"` // events API push
	Compare    string `json:"compare"`
	Repository *struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Sender  *client.User `json:"sender"`
	Commits []struct {
		ID      string `json:"id"`
		SHA     string `json:"sha"`
		Message string `json:"message"`
	} `json:"commits"`
	PullRequest *struct {
		Number  int         `json:"number"`
		Title   string      `json:"title"`
		State   string      `json:"state"`
		HTMLURL string      `json:"html_url"`
		Merged  bool        `json:"merged"`
		Draft   bool        `json:"draft"`
		User    client.User `json:"user"`
		Base    struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
	} `json:"pull_request"`
	Review *struct {
		State   string      `json:"state"`
		Body    string      `json:"body"`
		HTMLURL string      `json:"html_url"`
		User    client.User `json:"user"`
	} `json:"review"`
	Issue *struct {
		Number      int             `json:"number"`
		Title       string          `json:"title"`
		State       string          `json:"state"`
		HTMLURL     string          `json:"html_url"`
		User        client.User     `json:"user"`
		PullRequest json.RawMessage `json:"pull_request"`
		Labels      []struct {
			Name string `json:"name"`
		} `json:"labels"`
	} `json:"issue"`
	Comment *struct {
		Body    string      `json:"body"`
		HTMLURL string      `json:"html_url"`
		User    client.User `json:"user"`
	} `json:"comment"`
	Release *struct {
		TagName    string `json:"tag_name"`
		Name       string `json:"name"`
		HTMLURL    string `json:"html_url"`
		Prerelease bool   `json:"prerelease"`
	} `json:"release"`
	WorkflowRun *struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		HeadBranch string `json:"head_branch"`
		HTMLURL    string `json:"html_url"`
	} `json:"workflow_run"`
}

// toEvent builds the event of one GitHub event. The room is the
// repository; an issue or a pull request is the thread.
func toEvent(id, typ, ts, repo string, sender client.User, self *client.User, p payload, raw json.RawMessage) sdk.Event {
	ev := sdk.Event{
		ID: id, Type: typ, TS: ts,
		Room: &sdk.Room{ID: repo, Name: repo, Kind: sdk.RoomChannel},
		Sender: &sdk.Author{
			ID: sender.Login, Name: sender.Login, Bot: sender.Type == "Bot",
			Self: self != nil && sender.Login != "" && sender.Login == self.Login,
		},
	}
	if len(raw) <= maxRaw {
		ev.Raw = raw
	}
	data := map[string]any{"repository": repo}
	if p.Action != "" {
		data["action"] = p.Action
	}
	who := sender.Login
	thread := func(n int, title string) {
		ev.Thread = &sdk.Thread{ID: strconv.Itoa(n), Title: title}
		data["number"], data["title"] = n, title
	}
	switch typ {
	case "push":
		branch := strings.TrimPrefix(p.Ref, "refs/heads/")
		commits := make([]map[string]string, 0, len(p.Commits))
		lines := []string{fmt.Sprintf("%s pushed %d commit(s) to %s", who, len(p.Commits), branch)}
		for _, c := range p.Commits {
			sha := c.ID
			if sha == "" {
				sha = c.SHA
			}
			commits = append(commits, map[string]string{"id": sha, "message": c.Message})
			lines = append(lines, firstLine(c.Message))
		}
		data["ref"], data["branch"], data["head"], data["commits"] = p.Ref, branch, cmp.Or(p.After, p.Head), commits
		if p.Compare != "" {
			data["url"] = p.Compare
		}
		ev.Text = strings.Join(lines, "\n")
	case "pull_request":
		if pr := p.PullRequest; pr != nil {
			thread(pr.Number, pr.Title)
			data["state"], data["url"], data["merged"], data["draft"] = pr.State, pr.HTMLURL, pr.Merged, pr.Draft
			data["author"], data["base"], data["head"] = pr.User.Login, pr.Base.Ref, pr.Head.Ref
			ev.Text = fmt.Sprintf("%s %s pull request #%d: %s", who, p.Action, pr.Number, pr.Title)
		}
	case "pull_request_review":
		if pr := p.PullRequest; pr != nil {
			thread(pr.Number, pr.Title)
			data["url"] = pr.HTMLURL
		}
		if r := p.Review; r != nil {
			data["state"], data["url"], data["author"], data["body"] = r.State, r.HTMLURL, r.User.Login, r.Body
			ev.Text = fmt.Sprintf("%s reviewed #%v (%s)", who, data["number"], r.State)
			if r.Body != "" {
				ev.Text += "\n" + r.Body
			}
		}
	case "issues":
		if is := p.Issue; is != nil {
			thread(is.Number, is.Title)
			labels := make([]string, 0, len(is.Labels))
			for _, l := range is.Labels {
				labels = append(labels, l.Name)
			}
			data["state"], data["url"], data["author"], data["labels"] = is.State, is.HTMLURL, is.User.Login, labels
			ev.Text = fmt.Sprintf("%s %s issue #%d: %s", who, p.Action, is.Number, is.Title)
		}
	case "issue_comment":
		if is := p.Issue; is != nil {
			thread(is.Number, is.Title)
			data["is_pull_request"] = len(is.PullRequest) > 0 && string(is.PullRequest) != "null"
		}
		if c := p.Comment; c != nil {
			data["url"], data["author"], data["body"] = c.HTMLURL, c.User.Login, c.Body
			ev.Text = c.Body
		}
	case "release":
		if r := p.Release; r != nil {
			data["tag"], data["name"], data["url"], data["prerelease"] = r.TagName, r.Name, r.HTMLURL, r.Prerelease
			ev.Text = fmt.Sprintf("%s %s release %s", who, p.Action, cmp.Or(r.Name, r.TagName))
		}
	case "workflow_run":
		if r := p.WorkflowRun; r != nil {
			data["run_id"], data["name"], data["status"], data["conclusion"] = r.ID, r.Name, r.Status, r.Conclusion
			data["branch"], data["url"] = r.HeadBranch, r.HTMLURL
			ev.Text = fmt.Sprintf("Workflow %s on %s: %s", r.Name, r.HeadBranch, cmp.Or(r.Conclusion, r.Status))
		}
	}
	ev.Data, _ = json.Marshal(data) //nolint:errcheck // a map of plain values
	return ev
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
