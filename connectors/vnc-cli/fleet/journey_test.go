//go:build fleet || live

package fleet

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// bin is the vnc-cli binary that TestMain builds.
var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "vnc-cli-fleet")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	bin = filepath.Join(dir, "vnc-cli")
	if out, err := exec.Command("go", "build", "-o", bin, "..").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build vnc-cli: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// Scene keys and the click target of a scene, as goldens/click_targets.py.
var sceneKeys = []string{"0", "c", "d", "f", "g", "p", "s", "x"}

func clickTarget(key string) image.Point {
	for i, k := range sceneKeys {
		if k == key {
			return image.Pt(i%4*64+32, i/4*96+48)
		}
	}
	panic("no scene " + key)
}

// server is one computer of the journey.
type server struct {
	name string
	addr string
	// args are extra global flags, for example --tls-ca-cert or --user.
	args []string
	// env is added to the env of each command. It holds the password under
	// the name that args give with -p.
	env []string
	// keep are the env names of the test process that the commands keep.
	keep []string
	// size is the screen size. Zero is not known.
	size image.Point
	// scenes is true when the scene player of goldens/ shows the screen.
	scenes bool
	// resize is true when the server takes a resize from the client.
	resize bool
	// noResize is true when the server refuses a resize. When resize and
	// noResize are both false, the journey sends no resize.
	noResize bool
	// noPaste is the reason that the journey sends no paste. Empty sends it.
	noPaste string
	// watch are extra watch flags, for example a lower --threshold.
	watch []string
	// poke changes the screen for the watch step. Nil skips the watch
	// step, for a server that shows no desktop.
	poke func(j *journey)
	// events runs before the input steps. It returns the check that the
	// server got the input, for a server that shows no desktop.
	events func(t *testing.T) func(t *testing.T)
	// open runs before the journey. It returns the address and the function
	// that undoes it.
	open func(t *testing.T) (string, func())
}

// journey runs vnc-cli commands against one server.
type journey struct {
	t    *testing.T
	s    server
	home string
	dir  string
}

func newJourney(t *testing.T, s server) *journey {
	home, err := os.MkdirTemp("/tmp", "vf")
	if err != nil {
		t.Fatal(err)
	}
	j := &journey{t: t, s: s, home: home, dir: t.TempDir()}
	t.Cleanup(func() {
		j.run("--session=off", "session", "stop", "--all")
		os.RemoveAll(home)
	})
	return j
}

func (j *journey) env() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(j.s.keep, name) || !strings.HasPrefix(name, "VNC_") && !strings.HasPrefix(name, "TREBI_") && name != "HOME" {
			env = append(env, kv)
		}
	}
	return append(append(env, "HOME="+j.home), j.s.env...)
}

func (j *journey) command(ctx context.Context, args ...string) *exec.Cmd {
	full := append([]string{"-s", j.s.addr, "--timeout", "30s"}, j.s.args...)
	cmd := exec.CommandContext(ctx, bin, append(full, args...)...)
	cmd.Env = j.env()
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// run runs one command and returns stdout, stderr, and the exit code.
func (j *journey) run(args ...string) (string, string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := j.command(ctx, args...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		code = -1
	}
	return out.String(), errb.String(), code
}

func (j *journey) must(args ...string) string {
	j.t.Helper()
	out, errs, code := j.run(args...)
	if code != 0 {
		j.t.Fatalf("vnc-cli %s: exit %d: %s", strings.Join(args, " "), code, strings.TrimSpace(errs))
	}
	return out
}

func (j *journey) info(args ...string) image.Point {
	j.t.Helper()
	var in struct{ Width, Height int }
	out := j.must(append(args, "--json", "info")...)
	if err := json.Unmarshal([]byte(out), &in); err != nil {
		j.t.Fatalf("info %q: %v", out, err)
	}
	return image.Pt(in.Width, in.Height)
}

func (j *journey) capture(name string, args ...string) image.Image {
	j.t.Helper()
	file := filepath.Join(j.dir, name+".png")
	j.must(append(append([]string{"capture"}, args...), file)...)
	f, err := os.Open(file)
	if err != nil {
		j.t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		j.t.Fatalf("capture %s: %v", name, err)
	}
	return img
}

// scene is the path of a scene picture.
func scene(key string) string { return filepath.Join("goldens", "scenes", key+".png") }

// show presses key and waits for its scene.
func (j *journey) show(key string) {
	j.t.Helper()
	j.must("run", "--", "key", key, "expect", scene(key))
}

func (j *journey) do() {
	t := j.t
	if j.s.open != nil {
		addr, done := j.s.open(t)
		t.Cleanup(done)
		j.s.addr = addr
	}
	if j.s.scenes {
		t.Cleanup(func() { j.run("key", "0") })
	}
	t.Run("connect", func(t *testing.T) {
		j := j.with(t)
		// Once without the background session, then with it.
		sz := j.info("--session=off")
		if j.s.size != (image.Point{}) && sz != j.s.size {
			t.Fatalf("size %v, want %v", sz, j.s.size)
		}
		if sz2 := j.info(); sz2 != sz {
			t.Fatalf("session size %v, direct size %v", sz2, sz)
		}
	})
	t.Run("capture", func(t *testing.T) {
		j := j.with(t)
		if j.s.scenes {
			j.show("0")
		}
		img := j.capture("screen")
		if j.s.size != (image.Point{}) && img.Bounds().Size() != j.s.size {
			t.Fatalf("capture size %v, want %v", img.Bounds().Size(), j.s.size)
		}
		if r := j.capture("region", "-r", "8,8,32,16"); r.Bounds().Size() != image.Pt(32, 16) {
			t.Fatalf("region size %v", r.Bounds().Size())
		}
	})
	var events func(t *testing.T)
	if j.s.events != nil {
		events = j.s.events(t)
	}
	t.Run("type", func(t *testing.T) {
		j := j.with(t)
		j.must("type", "s")
		if j.s.scenes {
			j.must("expect", scene("s"))
		}
		j.must("key", "shift-a", "BackSpace")
	})
	t.Run("click", func(t *testing.T) {
		j := j.with(t)
		if !j.s.scenes {
			j.must("run", "--", "move", "10", "10", "click", "1")
			return
		}
		p := clickTarget("d")
		j.must("run", "--", "move", fmt.Sprint(p.X), fmt.Sprint(p.Y), "click", "1", "expect", scene("d"))
		j.show("0")
	})
	if events != nil {
		t.Run("events", events)
	}
	t.Run("clipboard", func(t *testing.T) {
		if j.s.noPaste != "" {
			t.Skip(j.s.noPaste)
		}
		j := j.with(t)
		// No fleet server sends the text of one client to the other
		// clients, so the journey cannot read back the text of paste.
		j.must("paste", fmt.Sprintf("vnc-cli fleet %d", time.Now().UnixNano()))
		j.must("clipboard", "get")
	})
	t.Run("resize", func(t *testing.T) {
		j := j.with(t)
		before := j.info()
		if j.s.noResize {
			// exit 30: the command failed.
			if _, errs, code := j.run("resize", "320", "240"); code != 30 {
				t.Fatalf("resize on a server that refuses it: exit %d: %s", code, errs)
			}
			if sz := j.info("--session=off"); sz != before {
				t.Fatalf("size after a refused resize %v, want %v", sz, before)
			}
			return
		}
		if !j.s.resize {
			t.Skip("the journey sends no resize to this server")
		}
		j.must("resize", "200", "150")
		if sz := j.info(); sz != image.Pt(200, 150) {
			t.Fatalf("size after resize %v", sz)
		}
		j.must("resize", fmt.Sprint(before.X), fmt.Sprint(before.Y))
		if sz := j.info("--session=off"); sz != before {
			t.Fatalf("size after the second resize %v, want %v", sz, before)
		}
	})
	t.Run("watch", func(t *testing.T) {
		if j.s.poke == nil {
			t.Skip("the server shows no desktop")
		}
		j.with(t).watch()
	})
}

// with is the journey for a subtest.
func (j *journey) with(t *testing.T) *journey {
	c := *j
	c.t = t
	return &c
}

// watch starts watch --once, pokes the screen after ready, and expects
// change and then stable.
func (j *journey) watch() {
	t := j.t
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := j.command(ctx, append([]string{"watch", "--once", "-d", "45s", "--settle", "500ms"}, j.s.watch...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stdout)
	var seen []string
	next := func() string {
		if !lines.Scan() {
			cmd.Wait()
			t.Fatalf("watch ended after %v: %s", seen, strings.TrimSpace(errb.String()))
		}
		var ev struct{ Event string }
		if err := json.Unmarshal(lines.Bytes(), &ev); err != nil {
			t.Fatalf("watch line %q: %v", lines.Text(), err)
		}
		seen = append(seen, ev.Event)
		return ev.Event
	}
	for next() != "ready" {
	}
	j.s.poke(j)
	for next() != "change" {
	}
	for next() != "stable" {
	}
	io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("watch: %v: %s", err, strings.TrimSpace(errb.String()))
	}
}

// pokeScene shows another scene.
func pokeScene(j *journey) {
	j.must("key", "x")
}
