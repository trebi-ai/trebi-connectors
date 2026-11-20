package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderBulkResult(t *testing.T) {
	var buf bytes.Buffer
	a := &AppContext{OutputFormat: "table"}
	body := `{"results":[{"index":0,"result":"updated","id":"a"},{"index":1,"result":"failed","id":"b","code":"not_found","detail":"gone"}],"succeeded":1,"failed":1}`
	if err := a.Render(&buf, []byte(body)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"RESULT", "not_found", "gone", "succeeded: 1  failed: 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderSingleListWrapper(t *testing.T) {
	var buf bytes.Buffer
	a := &AppContext{OutputFormat: "table", Columns: []string{"name"}}
	if err := a.Render(&buf, []byte(`{"issues":[{"name":"one"},{"name":"two"}]}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "NAME") || !strings.Contains(buf.String(), "two") {
		t.Errorf("out:\n%s", buf.String())
	}
}
