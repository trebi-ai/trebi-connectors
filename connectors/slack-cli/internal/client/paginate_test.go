package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/flarco/cli-tools/slack-cli/internal/config"
)

func TestParsePagePicksLargestList(t *testing.T) {
	body := []byte(`{"ok":true,"channels":[{"id":"C1"},{"id":"C2"}],"pins":[{"id":"P1"}],"response_metadata":{"next_cursor":"abc"}}`)
	p, ok := ParsePage(body)
	if !ok {
		t.Fatal("expected a page")
	}
	if p.Key != "channels" || len(p.Items) != 2 {
		t.Errorf("key=%q items=%d", p.Key, len(p.Items))
	}
	if p.NextCursor != "abc" {
		t.Errorf("cursor = %q", p.NextCursor)
	}
}

func TestParsePageNoList(t *testing.T) {
	if _, ok := ParsePage([]byte(`{"ok":true,"team":"Acme"}`)); ok {
		t.Error("expected no list")
	}
}

func TestListAllFollowsCursor(t *testing.T) {
	c := newTestClient(t, config.Workspace{Token: "xoxb-1"}, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.PostForm.Get("cursor") {
		case "":
			fmt.Fprint(w, `{"ok":true,"users":[{"id":"U1"}],"response_metadata":{"next_cursor":"p2"}}`)
		case "p2":
			fmt.Fprint(w, `{"ok":true,"users":[{"id":"U2"}],"response_metadata":{"next_cursor":""}}`)
		}
	})
	page, err := c.ListAll(context.Background(), "users.list", url.Values{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Key != "users" {
		t.Errorf("got %d items, key %q", len(page.Items), page.Key)
	}
}

func TestListAllRespectsMax(t *testing.T) {
	c := newTestClient(t, config.Workspace{Token: "xoxb-1"}, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"users":[{"id":"U1"},{"id":"U2"},{"id":"U3"}],"response_metadata":{"next_cursor":"more"}}`)
	})
	page, err := c.ListAll(context.Background(), "users.list", url.Values{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Errorf("max not applied: %d items", len(page.Items))
	}
}
