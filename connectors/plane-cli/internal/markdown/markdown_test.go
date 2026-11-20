package markdown

import (
	"errors"
	"strings"
	"testing"
)

func TestHTMLRendersGFM(t *testing.T) {
	src := "# T\n\n~~gone~~ https://example.com\n\n- [x] done\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```go\nx := 1\n```\n"
	out, err := New().HTML([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<h1>T</h1>", "<del>gone</del>", `<a href="https://example.com">`, `<li data-type="taskItem" data-checked="true">done</li>`, "<table>", `class="language-go"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name, src string
		line      int
		msg       string
	}{
		{"empty", " \n\t\n", 0, "empty"},
		{"bad utf8", "ok\n\xff\xfe\n", 2, "UTF-8"},
		{"nul", "a\nb\x00c", 2, "NUL"},
		{"unclosed backticks", "text\n\n```go\ncode\n", 3, "not closed"},
		{"unclosed tildes", "~~~~\ncode\n~~~\n", 1, "not closed"},
		{"wrong closer", "```\ncode\n~~~\n", 1, "not closed"},
		{"unclosed in list", "1. step\n   ```sh\n   run\n", 2, "not closed"},
	}
	for _, tc := range cases {
		_, err := New().HTML([]byte(tc.src))
		var pe *ParseError
		if !errors.As(err, &pe) || pe.Line != tc.line || !strings.Contains(pe.Msg, tc.msg) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
}

func TestValidateAccepts(t *testing.T) {
	for _, src := range []string{
		"plain text",
		"```\ncode\n```",
		"````md\n```\ninner\n```\n````\n",
		"> ```\n> quoted\n> ```\n",
		"- item\n  ```\n  x\n  ```\n",
		"inline ``` not a fence ``` here",
		"---\n\n***\n",
	} {
		if err := Validate([]byte(src)); err != nil {
			t.Errorf("%q: %v", src, err)
		}
	}
}

func TestTaskLists(t *testing.T) {
	cases := map[string]string{
		"- [x] a\n- [ ] b\n": `<ul data-type="taskList"><li data-type="taskItem" data-checked="true">a</li><li data-type="taskItem" data-checked="false">b</li></ul>`,
		"- [ ] a\n- b\n":     `<ul><li><input disabled="" type="checkbox"> a</li><li>b</li></ul>`,
		"1. [x] a\n":         `<ol><li><input checked="" disabled="" type="checkbox"> a</li></ol>`,
	}
	for src, want := range cases {
		got, err := New().HTML([]byte(src))
		if err != nil || got != want {
			t.Errorf("%q:\ngot  %q\nwant %q (err %v)", src, got, want, err)
		}
	}
}

func TestNoNewlinesBetweenBlocks(t *testing.T) {
	src := "# H\n\n**a**\nb\n\n> q\n\n| x |\n|---|\n| 1 |\n\n```\nl1\nl2\n```\n\n---\n"
	got, err := New().HTML([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := "<h1>H</h1><p><strong>a</strong>\nb</p><blockquote><p>q</p></blockquote>" +
		"<table><thead><tr><th>x</th></tr></thead><tbody><tr><td>1</td></tr></tbody></table>" +
		"<pre><code>l1\nl2</code></pre><hr>"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
