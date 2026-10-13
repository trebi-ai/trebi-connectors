package cmd_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/cmd"
	"github.com/trebi-ai/trebi-connectors/connectors/axiom-cli/internal/config"
)

func app() *cli.App {
	return &cli.App{
		Name:     "axiom-cli",
		Writer:   io.Discard,
		Flags:    []cli.Flag{&cli.StringFlag{Name: "token", EnvVars: []string{config.EnvToken}}, &cli.StringFlag{Name: "org-id"}, &cli.StringFlag{Name: "api-url"}},
		Commands: []*cli.Command{cmd.AuthCommand(), cmd.MonitorsCommand()},
	}
}

func TestTrebiModeRefusesSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TREBI_STATE_DIR", t.TempDir())
	t.Setenv(config.EnvToken, "")
	for _, args := range [][]string{{"auth", "set", "--token", "x"}, {"auth", "clear"}} {
		if err := app().Run(append([]string{"axiom-cli"}, args...)); !errors.Is(err, config.ErrSetInTrebi) {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".cli-tools")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a file was written: %v", err)
	}
	if err := app().Run([]string{"axiom-cli", "--token", "xaat-flag", "monitors", "list"}); !errors.Is(err, config.ErrTrebiSets) {
		t.Fatalf("got %v", err)
	}
}
