//go:build live

package fleet

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// liveServers parses VNC_LIVE_SERVERS: entries name=address,password_env[,user_env]
// with ; between them. The values stay in the env: the commands get the
// password by its env name, and the user name in VNC_USER.
func liveServers(spec string) ([]server, error) {
	var out []server
	for _, entry := range strings.Split(spec, ";") {
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		name, rest, ok := strings.Cut(entry, "=")
		f := strings.Split(rest, ",")
		if !ok || name == "" || len(f) < 2 || len(f) > 3 || f[0] == "" || f[1] == "" {
			return nil, fmt.Errorf("%q is not name=address,password_env[,user_env]", entry)
		}
		s := server{name: name, addr: f[0], args: []string{"-p", f[1]}, keep: []string{f[1]}, poke: pokeType}
		if os.Getenv(f[1]) == "" {
			return nil, fmt.Errorf("%s: the env variable %s is empty", name, f[1])
		}
		if len(f) == 3 {
			user := os.Getenv(f[2])
			if user == "" {
				return nil, fmt.Errorf("%s: the env variable %s is empty", name, f[2])
			}
			s.env = []string{"VNC_USER=" + user}
		}
		out = append(out, s)
	}
	return out, nil
}

// TestLiveJourney runs the journey on real computers. It types, clicks at
// 10,10, and pastes, so use a test account.
func TestLiveJourney(t *testing.T) {
	servers, err := liveServers(os.Getenv("VNC_LIVE_SERVERS"))
	if err != nil {
		t.Fatalf("VNC_LIVE_SERVERS: %v", err)
	}
	if len(servers) == 0 {
		t.Skip("VNC_LIVE_SERVERS is empty: set it to name=address,password_env[,user_env], with ; between entries")
	}
	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) { newJourney(t, s).do() })
	}
}

// pokeType types a space and removes it.
func pokeType(j *journey) {
	j.must("key", "space", "BackSpace")
}

func TestLiveServers(t *testing.T) {
	t.Setenv("LIVE_TEST_PW", "synthetic")
	t.Setenv("LIVE_TEST_USER", "tester")
	got, err := liveServers(" a=host::5900,LIVE_TEST_PW ; b=ws://h/x?y=1,LIVE_TEST_PW,LIVE_TEST_USER;")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].addr != "host::5900" || got[1].addr != "ws://h/x?y=1" || got[1].env[0] != "VNC_USER=tester" {
		t.Fatalf("parse: %+v", got)
	}
	for _, s := range got {
		if strings.Contains(strings.Join(append(s.args, s.env...), " "), "synthetic") {
			t.Fatalf("%s: the password is in the args or the env list", s.name)
		}
	}
	for _, bad := range []string{"a", "a=host", "a=host,", "a=host,LIVE_TEST_PW,X,Y", "a=host,NO_SUCH_ENV_VNC_LIVE"} {
		if _, err := liveServers(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
