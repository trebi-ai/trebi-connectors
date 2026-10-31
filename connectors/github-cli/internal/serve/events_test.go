package serve

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/fakegithub"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

// TestNewTypes sends one signed delivery of each type that 0.3 adds.
func TestNewTypes(t *testing.T) {
	t.Parallel()
	const secret = "s3cret"
	hook := &sdk.Webhook{URL: "http://127.0.0.1:1/in", Secret: secret}
	c, fake, _ := start(t, fakegithub.Token, hook)
	fake.NoDeliveries()
	repo := `"repository":{"full_name":"octo/app"},"sender":{"login":"hubot","type":"User"}`
	cases := []struct{ typ, body, text, data string }{
		{"pull_request_review_comment", `{"action":"created",` + repo + `,"pull_request":{"number":8,"title":"Add a cache"},"comment":{"body":"Use a map.","path":"a.go","line":3,"html_url":"u","user":{"login":"hubot"}}}`, "hubot commented on a.go in #8", `"line":3`},
		{"check_suite", `{"action":"completed",` + repo + `,"check_suite":{"id":5,"status":"completed","conclusion":"failure","head_branch":"main","head_sha":"abcdef123456","app":{"name":"GitHub Actions"},"pull_requests":[{"number":8}]}}`, "Checks of GitHub Actions on main: failure", `/commit/abcdef123456/checks`},
		{"status", `{` + repo + `,"sha":"abcdef123456","state":"error","context":"ci/build","description":"Build broke","target_url":"https://ci/1","branches":[{"name":"main"}]}`, "ci/build on abcdef1: error (Build broke)", `"branches":["main"]`},
		{"create", `{` + repo + `,"ref":"v1.2.0","ref_type":"tag"}`, "hubot created tag v1.2.0", `/tree/v1.2.0`},
		{"delete", `{` + repo + `,"ref":"old","ref_type":"branch"}`, "hubot deleted branch old", `"ref_type":"branch"`},
		{"deployment_status", `{"action":"created",` + repo + `,"deployment":{"id":9,"ref":"main","sha":"abc","environment":"production"},"deployment_status":{"state":"success","environment_url":"https://app"}}`, "Deployment to production: success", `"url":"https://app"`},
		{"discussion", `{"action":"created",` + repo + `,"discussion":{"number":4,"title":"Roadmap","html_url":"d","state":"open","category":{"name":"Ideas"},"user":{"login":"hubot"}}}`, "hubot created discussion #4: Roadmap", `"category":"Ideas"`},
		{"discussion_comment", `{"action":"created",` + repo + `,"discussion":{"number":4,"title":"Roadmap","category":{"name":"Ideas"}},"comment":{"body":"I agree.","html_url":"dc","user":{"login":"hubot"}}}`, "I agree.", `"author":"hubot"`},
	}
	var types []string
	for _, tc := range cases {
		types = append(types, tc.typ)
	}
	syncSubs(t, c, sub("s1", fakegithub.AdminRepo, types...))
	for i, tc := range cases {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(tc.body))
		req := sdk.WebhookRequest{ID: tc.typ, Method: "POST", Path: "/in", Body: tc.body, Headers: map[string]string{
			"x-github-event": tc.typ, "x-github-delivery": "d" + string(rune('a'+i)), "x-hub-signature-256": "sha256=" + hex.EncodeToString(mac.Sum(nil)),
		}}
		if err := c.Call(sdk.MethodWebhookReceive, req, nil); err != nil {
			t.Fatalf("%s: %v", tc.typ, err)
		}
		ev := nextEvent(t, c)
		if ev.Type != tc.typ || ev.Room.ID != fakegithub.AdminRepo || !strings.HasPrefix(ev.Text, tc.text) || !strings.Contains(string(ev.Data), tc.data) {
			t.Errorf("%s: %+v text %q data %s", tc.typ, ev, ev.Text, ev.Data)
		}
	}
}
