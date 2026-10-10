package main

import (
	"encoding/csv"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type testOperation struct {
	Name     string            `yaml:"name"`
	ReadOnly bool              `yaml:"readonly"`
	Default  string            `yaml:"default"`
	Command  []string          `yaml:"command"`
	Env      map[string]string `yaml:"env"`
}

func loadOperations(t *testing.T, name string) map[string]testOperation {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "catalog", name, ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Actions struct {
			Operations []testOperation `yaml:"operations"`
		} `yaml:"actions"`
	}
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	out := map[string]testOperation{}
	for _, op := range m.Actions.Operations {
		out[op.Name] = op
	}
	return out
}

// The states of D14: an agent can search, read, and draft, but not send.
func TestGmailOperationStates(t *testing.T) {
	ops := loadOperations(t, "gmail")
	want := map[string]struct {
		readonly bool
		state    string
	}{"search": {true, "on"}, "read": {true, "on"}, "draft": {false, "on"}, "send": {false, "off"}}
	for name, w := range want {
		op, ok := ops[name]
		if !ok {
			t.Fatalf("no operation %s", name)
		}
		if op.ReadOnly != w.readonly || op.Default != w.state {
			t.Errorf("%s: readonly %v default %q, want %v %q", name, op.ReadOnly, op.Default, w.readonly, w.state)
		}
		if op.Command[0] != "mailbox" || op.Command[len(op.Command)-1] != name {
			t.Errorf("%s: command %v", name, op.Command)
		}
	}
}

// The query of catalog/postgres runs psql with a read-only session and a
// statement timeout. The fake psql prints its argv and env.
func TestPostgresQueryIsReadOnly(t *testing.T) {
	op, ok := loadOperations(t, "postgres")["query"]
	if !ok || !op.ReadOnly {
		t.Fatalf("query: %+v", op)
	}
	sql := `select 'a; drop table x' as "v"`
	argv := make([]string, 0, len(op.Command))
	for _, el := range op.Command {
		argv = append(argv, strings.ReplaceAll(el, "{input.sql}", sql))
	}
	if argv[0] != "psql" {
		t.Fatalf("command %v", argv)
	}
	fake, err := filepath.Abs(filepath.Join("..", "..", "scripts", "fakes", "psql"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(fake, argv[1:]...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "PGHOST=db.example.com"}
	for k, v := range op.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	if err != nil {
		t.Fatalf("the fake output is not CSV: %v\n%s", err, out)
	}
	got := map[string]string{}
	var args []string
	for _, r := range recs[1:] {
		if r[0] == "arg" {
			args = append(args, r[2])
		} else {
			got[r[1]] = r[2]
		}
	}
	if !strings.Contains(got["PGOPTIONS"], "default_transaction_read_only=on") || !strings.Contains(got["PGOPTIONS"], "statement_timeout=") {
		t.Fatalf("PGOPTIONS %q", got["PGOPTIONS"])
	}
	if got["PGHOST"] != "db.example.com" {
		t.Fatalf("PGHOST %q", got["PGHOST"])
	}
	if args[len(args)-1] != sql || !strings.Contains(strings.Join(args, " "), "--csv") || !strings.Contains(strings.Join(args, " "), "--no-psqlrc") {
		t.Fatalf("args %q", args)
	}
}
