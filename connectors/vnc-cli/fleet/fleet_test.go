//go:build fleet

package fleet

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// The fleet password is synthetic. Each server gets it by env name.
const passwordEnv = "VNC_FLEET_PASSWORD"

var fleetEnv = []string{passwordEnv + "=vncdotool"}

func host() string {
	if h := os.Getenv("VNC_FLEET_HOST"); h != "" {
		return h
	}
	return "127.0.0.1"
}

func tcp(port int) string { return fmt.Sprintf("%s::%d", host(), port) }

var scene256 = image.Pt(256, 192)

// fleet is the journey of each server in docker-compose.yml. SERVERS.md
// tells the quirks. tigervnc-vencrypt-anon has no journey: Go has no
// anonymous TLS, and vnc/fleet_security_test.go checks its error.
func fleet() []server {
	auth := []string{"-p", passwordEnv}
	return []server{
		{name: "tigervnc", addr: tcp(5931), size: scene256, scenes: true, poke: pokeScene},
		{name: "tigervnc-auth", addr: tcp(5932), args: auth, env: fleetEnv, size: scene256, scenes: true, poke: pokeScene},
		{name: "tigervnc-vencrypt", addr: tcp(5941), args: append(auth, "--tls-ca-cert", "vencrypt-certs/cert.pem"), env: fleetEnv, size: scene256, scenes: true, poke: pokeScene},
		{name: "tigervnc-tlsvnc", addr: tcp(5942), args: auth, env: fleetEnv, size: scene256, scenes: true, poke: pokeScene},
		{name: "tigervnc-resize", addr: tcp(5940), size: scene256, scenes: true, resize: true, poke: pokeScene},
		{name: "x11vnc", addr: tcp(5933), noResize: true, size: scene256, scenes: true, poke: pokeScene},
		{name: "wayvnc", addr: tcp(5952), args: append(auth, "--user", "vncdotool", "--tls-ca-cert", "wayvnc-certs/cert.pem"), env: fleetEnv, size: scene256, scenes: true, noResize: true, poke: pokeScene,
			noPaste: "wayvnc 0.5.0 dies of SIGPIPE when it sends the clipboard after a client closed (SERVERS.md)"},
		{name: "selenoid", size: scene256, scenes: true, poke: pokeScene, open: selenoidSession},
		// The arrow keys resize the example for each later client, so the
		// journey sends none.
		{name: "libvncserver-example", addr: tcp(5935), noResize: true, size: image.Pt(800, 600), poke: pokePaint, watch: []string{"--threshold", "0.01"}},
		{name: "vncev", addr: tcp(5934), noResize: true, size: image.Pt(100, 100), events: vncevEvents},
		{name: "qemu", addr: fmt.Sprintf("ws://%s:5944/", host()), noResize: true, size: image.Pt(1280, 800), poke: pokeShell, watch: []string{"--threshold", "0.05"}},
		{name: "qemu-tls", addr: "wss://localhost:5945/", args: []string{"--tls-ca-cert", "qemu-tls/ca-cert.pem"}, noResize: true, size: image.Pt(1280, 800), poke: pokeShell, watch: []string{"--threshold", "0.05"}},
	}
}

func TestFleetJourney(t *testing.T) {
	for _, s := range fleet() {
		t.Run(s.name, func(t *testing.T) { newJourney(t, s).do() })
	}
}

// pokePaint draws a line: the example paints where the button is down. The
// line is new each time, because paint on old paint changes nothing.
func pokePaint(j *journey) {
	y := fmt.Sprint(150 + rand.IntN(400))
	j.must("run", "--", "move", "50", y, "mousedown", "1", "drag", "750", y, "mouseup", "1")
}

// pokeShell types a command in the UEFI shell of QEMU.
func pokeShell(j *journey) {
	j.must("run", "--", "type", "ver", "key", "enter")
}

// vncevEvents checks the log of vncev for the input of the type and the
// click steps.
func vncevEvents(t *testing.T) func(t *testing.T) {
	mark := len(vncevLog(t))
	return func(t *testing.T) {
		want := []*regexp.Regexp{
			regexp.MustCompile(`down: .*\(0x73\)`),
			regexp.MustCompile(`up: .*\(0x73\)`),
			regexp.MustCompile(`Ptr: mouse button mask 0x1 at 10,10`),
		}
		var log string
		for end := time.Now().Add(15 * time.Second); time.Now().Before(end); time.Sleep(500 * time.Millisecond) {
			log = vncevLog(t)[mark:]
			if !slices.ContainsFunc(want, func(re *regexp.Regexp) bool { return !re.MatchString(log) }) {
				return
			}
		}
		t.Fatalf("vncev did not log the input %v:\n%s", want, log)
	}
}

func vncevLog(t *testing.T) string {
	return compose(t, "logs", "--no-log-prefix", "vncev")
}

// compose runs docker compose on the fleet. VNC_FLEET_PROJECT is the compose
// project, vnc-cli-fleet by default.
func compose(t *testing.T, args ...string) string {
	project := os.Getenv("VNC_FLEET_PROJECT")
	if project == "" {
		project = "vnc-cli-fleet"
	}
	out, err := exec.Command("docker", append([]string{"compose", "-p", project, "-f", "docker-compose.yml"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// TestFleetClipboardEvent copies text in the desktop of Xvnc. watch writes
// one clipboard event with the text.
func TestFleetClipboardEvent(t *testing.T) {
	j := newJourney(t, server{name: "tigervnc-resize", addr: tcp(5940)})
	text := fmt.Sprintf("fleet clip %d", rand.Int())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := j.command(ctx, "--session=off", "watch", "--events", "clipboard", "-d", "8s")
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
	var got []string
	for lines.Scan() {
		var ev struct{ Event, Text string }
		if err := json.Unmarshal(lines.Bytes(), &ev); err != nil {
			t.Fatalf("watch line %q: %v", lines.Text(), err)
		}
		switch ev.Event {
		case "ready":
			compose(t, "exec", "-T", "-e", "DISPLAY=:0", "tigervnc-resize", "sh", "-c", `printf %s "$1" | xclip -selection clipboard`, "sh", text)
		case "clipboard":
			got = append(got, ev.Text)
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("watch: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	if len(got) != 1 || got[0] != text {
		t.Fatalf("clipboard events %q, want one with %q", got, text)
	}
}

// selenoidSession opens a WebDriver session. The VNC address works only
// while it is open.
func selenoidSession(t *testing.T) (string, func()) {
	base := fmt.Sprintf("http://%s:5946/wd/hub/session", host())
	body := `{"capabilities":{"alwaysMatch":{"browserName":"stub","browserVersion":"1.0","selenoid:options":{"enableVNC":true}}}}`
	cl := &http.Client{Timeout: 30 * time.Second}
	resp, err := cl.Post(base, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("open a Selenoid session: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		SessionID string `json:"sessionId"`
		Value     struct {
			SessionID string `json:"sessionId"`
		} `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("open a Selenoid session: status %d: %v", resp.StatusCode, err)
	}
	id := out.SessionID
	if id == "" {
		id = out.Value.SessionID
	}
	addr := fmt.Sprintf("ws://%s:5946/vnc/%s?password=vncdotool", host(), id)
	return addr, func() {
		req, _ := http.NewRequest(http.MethodDelete, base+"/"+id, nil)
		if resp, err := cl.Do(req); err == nil {
			resp.Body.Close()
		}
	}
}
