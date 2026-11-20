package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/config"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(&config.Config{APIKey: "k", Host: srv.URL})
}

func TestDoSendsAPIKeyAndBody(t *testing.T) {
	cl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "k" {
			t.Errorf("missing api key header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type = %q", r.Header.Get("Content-Type"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "x" || r.URL.Query().Get("fields") != "id" {
			t.Errorf("body=%v query=%v", body, r.URL.Query())
		}
		w.WriteHeader(201)
		fmt.Fprint(w, `{"id":"1"}`)
	})
	resp, err := cl.Do(context.Background(), Request{Method: "POST", Path: "/api/v2/x/", Query: url.Values{"fields": {"id"}}, Body: map[string]any{"name": "x"}})
	if err != nil || resp.Status != 201 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
}

func TestProblemJSONError(t *testing.T) {
	cl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"type":"invalid_request","code":"invalid_request","detail":"bad","errors":[{"field":"name","code":"required","message":"This field is required."}]}`)
	})
	_, err := cl.Do(context.Background(), Request{Method: "GET", Path: "/api/v2/x/"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "invalid_request" || len(apiErr.Errors) != 1 {
		t.Fatalf("err = %#v", err)
	}
	if !strings.Contains(err.Error(), "name: This field is required.") {
		t.Errorf("message = %q", err.Error())
	}
}

func TestV1ErrorFallsBackToBody(t *testing.T) {
	cl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error": "Page not found."}`)
	})
	_, err := cl.Do(context.Background(), Request{Method: "GET", Path: "/api/v1/x/"})
	if err == nil || !strings.Contains(err.Error(), "Page not found.") {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryOn429(t *testing.T) {
	var n atomic.Int32
	cl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		fmt.Fprint(w, `{}`)
	})
	if _, err := cl.Do(context.Background(), Request{Method: "GET", Path: "/api/v2/x/"}); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 {
		t.Errorf("calls = %d", n.Load())
	}
}

func TestParsePage(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		items   int
		hasMore bool
		offset  *int
		cursor  string
	}{
		{"v2 offset", `{"data":[{},{}],"next":2,"previous":null,"total_count":5,"pagination":{"style":"offset"}}`, 2, true, intp(2), ""},
		{"v2 offset last", `{"data":[{}],"next":null,"pagination":{"style":"offset"}}`, 1, false, nil, ""},
		{"v2 cursor", `{"data":[{}],"next_cursor":"abc","has_more":true,"pagination":{"style":"cursor"}}`, 1, true, nil, "abc"},
		{"v1 cursor", `{"results":[{},{}],"next_cursor":"100:1:0","next_page_results":true}`, 2, true, nil, "100:1:0"},
		{"v1 last", `{"results":[{}],"next_cursor":"100:1:0","next_page_results":false}`, 1, false, nil, "100:1:0"},
	}
	for _, tc := range cases {
		p := ParsePage([]byte(tc.body))
		if !p.Paginated || len(p.Items) != tc.items || p.HasMore != tc.hasMore || p.NextCursor != tc.cursor {
			t.Errorf("%s: %+v", tc.name, p)
		}
		if (tc.offset == nil) != (p.NextOffset == nil) || (tc.offset != nil && *tc.offset != *p.NextOffset) {
			t.Errorf("%s: offset %v", tc.name, p.NextOffset)
		}
	}
	if p := ParsePage([]byte(`{"id":"x"}`)); p.Paginated {
		t.Errorf("object parsed as page")
	}
	if p := ParsePage([]byte(`[{"id":"x"}]`)); p.Paginated || len(p.Items) != 1 {
		t.Errorf("bare array: %+v", p)
	}
}

func intp(i int) *int { return &i }

func TestListAllCursor(t *testing.T) {
	cl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch q.Get("cursor") {
		case "":
			if q.Get("paginate") != "cursor" || q.Get("per_page") != "200" {
				t.Errorf("first query = %v", q)
			}
			fmt.Fprint(w, `{"data":[{"id":"1"}],"next_cursor":"c2","has_more":true,"pagination":{"style":"cursor"}}`)
		case "c2":
			fmt.Fprint(w, `{"data":[{"id":"2"}],"next_cursor":null,"has_more":false,"pagination":{"style":"cursor"}}`)
		}
	})
	items, err := cl.ListAll(context.Background(), "/api/v2/x/", nil, true, 0)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
}

func TestListAllFallsBackToOffset(t *testing.T) {
	cl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("paginate") == "cursor" {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"type":"invalid_request","code":"ordering_not_cursor_eligible","detail":"no"}`)
			return
		}
		switch q.Get("offset") {
		case "":
			fmt.Fprint(w, `{"data":[{"id":"1"},{"id":"2"}],"next":2,"pagination":{"style":"offset"}}`)
		case "2":
			fmt.Fprint(w, `{"data":[{"id":"3"}],"next":null,"pagination":{"style":"offset"}}`)
		}
	})
	items, err := cl.ListAll(context.Background(), "/api/v2/x/", url.Values{"order_by": {"priority"}}, true, 0)
	if err != nil || len(items) != 3 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	items, err = cl.ListAll(context.Background(), "/api/v2/x/", nil, true, 2)
	if err != nil || len(items) != 2 {
		t.Fatalf("max: items=%d err=%v", len(items), err)
	}
}

func TestListAllV1(t *testing.T) {
	cl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("paginate") != "" {
			t.Errorf("v1 got paginate")
		}
		if r.URL.Query().Get("cursor") == "" {
			fmt.Fprint(w, `{"results":[{"id":"1"}],"next_cursor":"n","next_page_results":true}`)
			return
		}
		fmt.Fprint(w, `{"results":[{"id":"2"}],"next_cursor":"n2","next_page_results":false}`)
	})
	items, err := cl.ListAll(context.Background(), "/api/v1/x/", nil, false, 0)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
}

func TestResolvers(t *testing.T) {
	var calls atomic.Int32
	cl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/api/v2/workspaces/acme/projects/ENG/":
			fmt.Fprint(w, `{"id":"11111111-1111-1111-1111-111111111111"}`)
		case "/api/v2/workspaces/acme/work-items/ENG-7/":
			fmt.Fprint(w, `{"id":"22222222-2222-2222-2222-222222222222"}`)
		default:
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		id, err := cl.ProjectID(ctx, "acme", "ENG")
		if err != nil || id != "11111111-1111-1111-1111-111111111111" {
			t.Fatalf("project id=%q err=%v", id, err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("project lookup not cached: %d calls", calls.Load())
	}
	id, err := cl.WorkItemID(ctx, "acme", "ENG-7")
	if err != nil || id != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("work item id=%q err=%v", id, err)
	}
	if id, _ := cl.ProjectID(ctx, "acme", "33333333-3333-3333-3333-333333333333"); id != "33333333-3333-3333-3333-333333333333" {
		t.Errorf("uuid passthrough = %q", id)
	}
	if p, s, ok := SplitIdentifier("ENG-42"); !ok || p != "ENG" || s != "42" {
		t.Errorf("split = %q %q %v", p, s, ok)
	}
	if _, _, ok := SplitIdentifier("11111111-1111-1111-1111-111111111111"); ok {
		t.Errorf("uuid parsed as identifier")
	}
}

func TestUploadFlow(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		got = map[string]string{"key": r.FormValue("key")}
		f, h, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		got["file"] = h.Filename
		w.WriteHeader(204)
	}))
	defer srv.Close()
	body := []byte(`{"asset_id":"a1","upload_data":{"url":"` + srv.URL + `","fields":{"key":"k1"}}}`)
	target, ok := FindUploadTarget(body)
	if !ok {
		t.Fatal("no upload target")
	}
	cl := New(&config.Config{APIKey: "k"})
	if err := cl.Upload(context.Background(), target, "a.txt", strings.NewReader("hi")); err != nil {
		t.Fatal(err)
	}
	if got["key"] != "k1" || got["file"] != "a.txt" {
		t.Errorf("form = %v", got)
	}
	nested := []byte(`{"attachment":{"id":"x"},"meta":{"upload_data":{"url":"u","fields":{}}}}`)
	if _, ok := FindUploadTarget(nested); !ok {
		t.Errorf("nested upload_data not found")
	}
}

func TestDownloadFollowsRedirectWithoutAPIKey(t *testing.T) {
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "" {
			t.Errorf("API key sent to storage")
		}
		fmt.Fprint(w, "file body")
	}))
	defer storage.Close()
	cl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "k" {
			t.Errorf("missing API key")
		}
		http.Redirect(w, r, storage.URL+"/obj?sig=1", http.StatusFound)
	})
	var buf strings.Builder
	n, err := cl.Download(context.Background(), "/api/v1/x/", &buf)
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != "file body" || n != 9 {
		t.Errorf("got %q (%d bytes)", buf.String(), n)
	}

	cl = newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":"File not uploaded"}`)
	})
	if _, err := cl.Download(context.Background(), "/api/v1/x/", &buf); err == nil {
		t.Errorf("want error for status 400")
	}
}

func TestDownloadAssetURLFromJSON(t *testing.T) {
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "" {
			t.Errorf("API key sent to storage")
		}
		fmt.Fprint(w, "asset body")
	}))
	defer storage.Close()
	cl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"asset_id":"a1","asset_url":%q,"asset_name":"a.png"}`, storage.URL+"/obj?sig=1")
	})
	var buf strings.Builder
	if _, err := cl.Download(context.Background(), "/api/v1/x/", &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "asset body" {
		t.Errorf("got %q", buf.String())
	}

	cl = newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"asset_url":"/api/assets/v2/x/"}`)
	})
	if _, err := cl.Download(context.Background(), "/api/v1/x/", &buf); err == nil {
		t.Errorf("want error for a relative asset_url")
	}
}
