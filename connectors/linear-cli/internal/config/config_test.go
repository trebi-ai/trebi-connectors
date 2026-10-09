package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(work, ".env"), []byte("LINEAR_API_KEY=from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Chdir(work)
	cases := []struct {
		name, stateDir, env, flag string
		want                      Settings
		err                       error
	}{
		{name: "trebi env", stateDir: t.TempDir(), env: "k-trebi", want: Settings{Key: "k-trebi", Source: "Trebi (LINEAR_API_KEY)", Trebi: true}},
		{name: "trebi skips dotenv", stateDir: t.TempDir(), want: Settings{Trebi: true}},
		{name: "trebi rejects flag", stateDir: t.TempDir(), env: "k", flag: "f", err: ErrTrebiSets},
		{name: "standalone flag", env: "k", flag: "f", want: Settings{Key: "f", Source: "flag (--key)"}},
		{name: "standalone env", env: "k", want: Settings{Key: "k", Source: "env (LINEAR_API_KEY)"}},
		{name: "standalone dotenv", want: Settings{Key: "from-dotenv", Source: ".env (" + filepath.Join(work, ".env") + ")"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("TREBI_STATE_DIR", c.stateDir)
			t.Setenv(EnvKey, c.env)
			got, err := Resolve(c.flag)
			if c.err != nil {
				if !errors.Is(err, c.err) {
					t.Fatalf("err %v, want %v", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Key != c.want.Key || got.Trebi != c.want.Trebi || (c.want.Source != "" && got.Source != c.want.Source) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}
