package cmd

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc/vnctest"
)

// standalone clears the Trebi env and points HOME at a temp folder.
func standalone(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"TREBI_STATE_DIR", "TREBI_CACHE_DIR", config.EnvServer, config.EnvUser, config.EnvPassword, config.EnvIdleTimeout} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func server(t *testing.T, o vnctest.Options) (*vnctest.Server, string) {
	t.Helper()
	s := vnctest.New(o)
	addr, err := s.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, addr
}

type result struct {
	stdout, stderr string
	err            error
}

func (r result) code() int { return ExitCode(r.err) }

func run(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	r := NewRunner(strings.NewReader(stdin), &out, &errb)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := r.App("test").Run(ctx, append([]string{"vnc-cli"}, args...))
	return result{out.String(), errb.String(), err}
}

func mustRun(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	res := run(t, stdin, args...)
	if res.err != nil {
		t.Fatalf("vnc-cli %s: %v (exit %d)", strings.Join(args, " "), res.err, res.code())
	}
	return res
}

func keys(events []vnctest.InputEvent) []vnctest.InputEvent {
	var out []vnctest.InputEvent
	for _, e := range events {
		if e.Kind == "key" {
			out = append(out, vnctest.InputEvent{Kind: "key", Keysym: e.Keysym, Down: e.Down})
		}
	}
	return out
}

func press(ks ...uint32) []vnctest.InputEvent {
	var out []vnctest.InputEvent
	for _, k := range ks {
		out = append(out, vnctest.InputEvent{Kind: "key", Keysym: k, Down: true})
	}
	for i := len(ks) - 1; i >= 0; i-- {
		out = append(out, vnctest.InputEvent{Kind: "key", Keysym: ks[i]})
	}
	return out
}

func waitEvents(t *testing.T, s *vnctest.Server, n int) []vnctest.InputEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ev, err := s.WaitEvents(ctx, n)
	if err != nil {
		t.Fatalf("got %d events, want %d: %+v", len(s.Events()), n, s.Events())
	}
	return ev
}

func equalEvents(t *testing.T, got, want []vnctest.InputEvent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("events:\n got %+v\nwant %+v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("event %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestKeys(t *testing.T) {
	standalone(t)
	s, addr := server(t, vnctest.Options{})
	mustRun(t, "", "-s", addr, "key", "ctrl-alt-del", "a")
	mustRun(t, "", "-s", addr, "key", "down", "shift")
	mustRun(t, "", "-s", addr, "key", "up", "shift")
	mustRun(t, "", "-s", addr, "key", "--force-caps", "A")
	want := append(press(vnc.KeyControlL, vnc.KeyAltL, vnc.KeyDelete), press('a')...)
	want = append(want, vnctest.InputEvent{Kind: "key", Keysym: vnc.KeyShiftL, Down: true}, vnctest.InputEvent{Kind: "key", Keysym: vnc.KeyShiftL})
	want = append(want, press(vnc.KeyShiftL, 'A')...)
	equalEvents(t, keys(waitEvents(t, s, len(want))), want)

	if res := run(t, "", "-s", addr, "key", "nokey"); res.code() != ExitUsage {
		t.Fatalf("unknown key: exit %d, want %d", res.code(), ExitUsage)
	}
}

func TestType(t *testing.T) {
	standalone(t)
	s, addr := server(t, vnctest.Options{})
	t.Setenv("SECRET_FOR_TEST", "pw")
	file := filepath.Join(t.TempDir(), "text")
	os.WriteFile(file, []byte("f\n"), 0o600)

	mustRun(t, "", "-s", addr, "type", "a-B", "--enter")
	mustRun(t, "", "-s", addr, "type", "--env", "SECRET_FOR_TEST")
	mustRun(t, "", "-s", addr, "type", "--file", file)
	mustRun(t, "x\n", "-s", addr, "--delay", "1-3", "type", "-")
	mustRun(t, "", "-s", addr, "type", "--human", "o k")
	var want []vnctest.InputEvent
	for _, k := range []uint32{'a', '-', 'B', vnc.KeyReturn, 'p', 'w', 'f', 'x', 'o', ' ', 'k'} {
		want = append(want, press(k)...)
	}
	equalEvents(t, keys(waitEvents(t, s, len(want))), want)

	for _, args := range [][]string{{"type"}, {"type", "a", "--env", "SECRET_FOR_TEST"}, {"type", "--env", "NOT_SET_FOR_TEST"}} {
		if res := run(t, "", append([]string{"-s", addr}, args...)...); res.code() != ExitUsage {
			t.Fatalf("%v: exit %d, want %d", args, res.code(), ExitUsage)
		}
	}
}

func TestMouse(t *testing.T) {
	standalone(t)
	s, addr := server(t, vnctest.Options{Width: 100, Height: 100})
	mustRun(t, "", "-s", addr, "run", "--", "move", "10", "20", "click", "3")
	mustRun(t, "", "-s", addr, "mouse", "down", "1")
	mustRun(t, "", "-s", addr, "mouse", "up", "1")
	mustRun(t, "", "-s", addr, "scroll", "0", "-1")
	mustRun(t, "", "-s", addr, "drag", "--step", "10", "20", "0")
	ev := waitEvents(t, s, 11)
	type p struct {
		x, y int
		b    uint8
	}
	var got []p
	for _, e := range ev {
		if e.Kind == "pointer" {
			got = append(got, p{e.X, e.Y, e.Buttons})
		}
	}
	want := []p{
		{10, 20, 0}, {10, 20, 4}, {10, 20, 0}, // run: move, click 3
		{0, 0, 1}, {0, 0, 0}, // a new connection starts at 0,0
		{0, 0, 8}, {0, 0, 0}, // wheel up
		{0, 0, 1}, {10, 0, 1}, {20, 0, 1}, {20, 0, 0}, // drag
	}
	if len(got) != len(want) {
		t.Fatalf("pointer events:\n got %v\nwant %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("pointer event %d: got %v, want %v (all %v)", i, got[i], want[i], got)
		}
	}
}

func TestCapture(t *testing.T) {
	standalone(t)
	s, addr := server(t, vnctest.Options{Width: 40, Height: 30})
	s.Paint(image.Rect(5, 5, 15, 10), color.RGBA{200, 10, 10, 255})
	dir := t.TempDir()
	for _, name := range []string{"a.png", "a.jpg", "a.gif", "a.bmp"} {
		mustRun(t, "", "-s", addr, "capture", filepath.Join(dir, name))
	}
	got := decode(t, filepath.Join(dir, "a.png"))
	if !vnc.Match(got, s.Screen(), 0, 0) {
		t.Fatal("the PNG differs from the screen")
	}
	mustRun(t, "", "-s", addr, "capture", "-r", "5,5,10,5", filepath.Join(dir, "r.png"))
	if r := decode(t, filepath.Join(dir, "r.png")); r.Bounds().Dx() != 10 || r.Bounds().Dy() != 5 || r.At(0, 0) != (color.RGBA{200, 10, 10, 255}) {
		t.Fatalf("region: %v %v", r.Bounds(), r.At(0, 0))
	}
	res := mustRun(t, "", "-s", addr, "capture", "-")
	if _, err := png.Decode(strings.NewReader(res.stdout)); err != nil {
		t.Fatalf("stdout is not a PNG: %v", err)
	}
	if res := run(t, "", "-s", addr, "capture", filepath.Join(dir, "a.tiff")); res.code() != ExitUsage {
		t.Fatalf("tiff: exit %d", res.code())
	}
	if res := run(t, "", "-s", addr, "capture", "-r", "30,20,20,20", filepath.Join(dir, "o.png")); res.code() != ExitFailed {
		t.Fatalf("off screen: exit %d (%v)", res.code(), res.err)
	}
	res = mustRun(t, "", "-s", addr, "-j", "info")
	if !strings.Contains(res.stdout, `"width":40`) {
		t.Fatalf("info: %s", res.stdout)
	}
}

func decode(t *testing.T, file string) image.Image {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestClipboard(t *testing.T) {
	standalone(t)
	s, addr := server(t, vnctest.Options{})
	mustRun(t, "", "-s", addr, "paste", "héllo")
	mustRun(t, "line\r\n", "-s", addr, "paste", "-")
	ev := waitEvents(t, s, 2)
	if ev[0].Text != "héllo" || ev[1].Text != "line\n" {
		t.Fatalf("cut events: %+v", ev)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			s.CutText("from server")
			time.Sleep(20 * time.Millisecond)
		}
	}()
	res := mustRun(t, "", "-s", addr, "clipboard", "get")
	if res.stdout != "from server" {
		t.Fatalf("clipboard get: %q", res.stdout)
	}
}

func TestExpectAndStable(t *testing.T) {
	standalone(t)
	s, addr := server(t, vnctest.Options{Width: 40, Height: 30})
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref.png")
	mustRun(t, "", "-s", addr, "capture", ref)
	mustRun(t, "", "-s", addr, "--timeout", "5s", "expect", ref)

	part := filepath.Join(dir, "part.png")
	mustRun(t, "", "-s", addr, "capture", "-r", "10,10,5,5", part)
	s.Paint(image.Rect(0, 0, 40, 30), color.RGBA{0, 0, 255, 255})
	if res := run(t, "", "-s", addr, "--timeout", "300ms", "expect", ref); res.code() != ExitTimeout {
		t.Fatalf("expect a wrong screen: exit %d (%v)", res.code(), res.err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		s.Paint(image.Rect(0, 0, 40, 30), color.RGBA{0, 0, 0, 255})
	}()
	mustRun(t, "", "-s", addr, "--timeout", "5s", "expect", "-r", "10,10", part)

	start := time.Now()
	mustRun(t, "", "-s", addr, "--timeout", "5s", "stable", "0.3")
	if d := time.Since(start); d < 300*time.Millisecond {
		t.Fatalf("stable returned after %v", d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Animate(ctx, 30*time.Millisecond)
	if res := run(t, "", "-s", addr, "--timeout", "500ms", "stable", "0.3"); res.code() != ExitTimeout {
		t.Fatalf("stable on a changing screen: exit %d (%v)", res.code(), res.err)
	}
}

func TestRunScript(t *testing.T) {
	standalone(t)
	s, addr := server(t, vnctest.Options{})
	dir := t.TempDir()
	inner := filepath.Join(dir, "inner.vdo")
	os.WriteFile(inner, []byte("key b # a comment\n"), 0o600)
	script := filepath.Join(dir, "main.vdo")
	os.WriteFile(script, []byte("key a\n"+inner+"\ntype 'c d'\npause 0.01\n"), 0o600)
	mustRun(t, "", "-s", addr, "run", script)
	var want []vnctest.InputEvent
	for _, k := range []uint32{'a', 'b', 'c', ' ', 'd'} {
		want = append(want, press(k)...)
	}
	equalEvents(t, keys(waitEvents(t, s, len(want))), want)

	loop := filepath.Join(dir, "loop.vdo")
	os.WriteFile(loop, []byte(loop+"\n"), 0o600)
	for _, args := range [][]string{{"run", "--", "nocommand"}, {"run", "--", "move", "1"}, {"run", "--", "expect", "x.png", "1.5"}, {"run", loop}} {
		if res := run(t, "", append([]string{"-s", addr}, args...)...); res.code() != ExitUsage {
			t.Fatalf("%v: exit %d (%v)", args, res.code(), res.err)
		}
	}
}

func TestSplitScript(t *testing.T) {
	got, err := splitScript("type \"a \\\"b\" # c\nkey 'x y'\\ z\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"type", `a "b`, "key", "x y z"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
	if _, err := splitScript("type 'a"); err == nil {
		t.Fatal("an open quote gives no error")
	}
}

func TestExitCodes(t *testing.T) {
	standalone(t)
	_, addr := server(t, vnctest.Options{Security: []uint8{vnc.SecVNC}, Password: "right"})
	t.Setenv("PW_FOR_TEST", "wrong-secret")
	res := run(t, "", "-s", addr, "-p", "PW_FOR_TEST", "info")
	if res.code() != ExitAuth || strings.Contains(res.err.Error(), "wrong-secret") {
		t.Fatalf("wrong password: exit %d, %v", res.code(), res.err)
	}
	t.Setenv("PW_FOR_TEST", "right")
	mustRun(t, "", "-s", addr, "-p", "PW_FOR_TEST", "info")

	if res := run(t, "", "-s", "127.0.0.1::1", "info"); res.code() != ExitConnect {
		t.Fatalf("refused: exit %d (%v)", res.code(), res.err)
	}
	if res := run(t, "", "info"); res.code() != ExitUsage {
		t.Fatalf("no server: exit %d (%v)", res.code(), res.err)
	}
	if res := run(t, "", "nocommand"); res.code() != ExitUsage {
		t.Fatalf("unknown command: exit %d (%v)", res.code(), res.err)
	}
}

func TestStandaloneConfig(t *testing.T) {
	home := standalone(t)
	_, addr := server(t, vnctest.Options{Security: []uint8{vnc.SecVNC}, Password: "pw", Name: "lab"})
	t.Setenv("LAB_PW", "pw")
	mustRun(t, "", "config", "set", "lab", "--address", addr, "--secret-env", "LAB_PW", "--default")
	b, err := os.ReadFile(filepath.Join(home, ".cli-tools", "vnc-cli", "config.toml"))
	if err != nil || !strings.Contains(string(b), "password_env") || strings.Contains(string(b), `"pw"`) {
		t.Fatalf("config file: %s %v", b, err)
	}
	if res := mustRun(t, "", "info"); !strings.Contains(res.stdout, "name: lab") {
		t.Fatalf("default target: %s", res.stdout)
	}
	if res := mustRun(t, "", "-t", "lab", "info"); !strings.Contains(res.stdout, "name: lab") {
		t.Fatalf("named target: %s", res.stdout)
	}
	if res := run(t, "", "-t", "nolab", "info"); res.code() != ExitUsage {
		t.Fatalf("unknown target: exit %d", res.code())
	}
	mustRun(t, "", "config", "rm", "lab")
	if res := run(t, "", "info"); res.code() != ExitUsage {
		t.Fatalf("removed target: exit %d (%v)", res.code(), res.err)
	}
}

func TestTrebiMode(t *testing.T) {
	home := standalone(t)
	_, addr := server(t, vnctest.Options{Security: []uint8{vnc.SecVNC}, Password: "pw", Name: "trebi"})
	_, other := server(t, vnctest.Options{Name: "personal"})
	dir := filepath.Join(home, ".cli-tools", "vnc-cli")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte("default = \"p\"\n[targets.p]\nserver = \""+other+"\"\n"), 0o600)
	t.Setenv("TREBI_STATE_DIR", t.TempDir())

	if res := run(t, "", "info"); res.code() != ExitUsage || !errors.As(res.err, new(usageError)) {
		t.Fatalf("no input: exit %d (%v)", res.code(), res.err)
	}
	t.Setenv(config.EnvServer, addr)
	t.Setenv(config.EnvPassword, "pw")
	if res := mustRun(t, "", "info"); !strings.Contains(res.stdout, "name: trebi") {
		t.Fatalf("Trebi mode used another server: %s", res.stdout)
	}
	for _, args := range [][]string{{"-s", other, "info"}, {"-t", "p", "info"}, {"-p", "X", "info"}} {
		res := run(t, "", args...)
		if !errors.Is(res.err, config.ErrTrebiSets) {
			t.Fatalf("%v: %v", args, res.err)
		}
	}
	for _, args := range [][]string{{"config", "set", "x", "--address", other}, {"config", "rm", "p"}} {
		if res := run(t, "", args...); !errors.Is(res.err, config.ErrSetInTrebi) {
			t.Fatalf("%v: %v", args, res.err)
		}
	}
}
