//go:build fleet

// Run with -p 1: this test changes the scene that the vnc fleet tests read.
package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/watch"
)

func TestFleetSceneScript(t *testing.T) {
	standalone(t)
	host := os.Getenv("VNC_FLEET_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	addr := host + "::5931"
	t.Chdir(filepath.Join("..", "fleet", "goldens"))
	defer run(t, "", "-s", addr, "--timeout", "10s", "run", "--", "key", "0", "expect", "scenes/0.png")
	mustRun(t, "", "-s", addr, "--timeout", "60s", "run", "scene.vdo")
	mustRun(t, "", "-s", addr, "--timeout", "10s", "run", "--", "key", "s", "stable", "0.3", "rexpect", "scenes/s.png", "0", "0")
}

func TestFleetWatch(t *testing.T) {
	standalone(t)
	host := os.Getenv("VNC_FLEET_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	addr := host + "::5931"
	defer run(t, "", "-s", addr, "key", "0")
	w := startWatch(t, "-s", addr, "--session=off", "watch", "--settle", "300ms")
	w.expect(watch.KindReady)
	mustRun(t, "", "-s", addr, "key", "s")
	w.expect(watch.KindChange)
	if ev := w.expect(watch.KindStable); ev.Pixels == 0 {
		t.Fatalf("stable = %+v", ev)
	}
}
