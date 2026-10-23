package vnc

import "testing"

func TestParseAddr(t *testing.T) {
	for in, want := range map[string]Addr{
		"host":                           {"tcp", "host:5900"},
		"host:1":                         {"tcp", "host:5901"},
		"host:99":                        {"tcp", "host:5999"},
		"host:5902":                      {"tcp", "host:5902"},
		"host::5":                        {"tcp", "host:5"},
		":2":                             {"tcp", "localhost:5902"},
		"10.0.0.1::5900":                 {"tcp", "10.0.0.1:5900"},
		"[::1]":                          {"tcp", "[::1]:5900"},
		"[::1]:3":                        {"tcp", "[::1]:5903"},
		"[fe80::1]::6000":                {"tcp", "[fe80::1]:6000"},
		"/tmp/vnc.sock":                  {"unix", "/tmp/vnc.sock"},
		"unix:/run/v.sock":               {"unix", "/run/v.sock"},
		"ws://h:6080/websockify?token=a": {"ws", "ws://h:6080/websockify?token=a"},
		"wss://h/x":                      {"wss", "wss://h/x"},
	} {
		got, err := ParseAddr(in)
		if err != nil || got != want {
			t.Errorf("%q: got %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "host:x", "host::0", "host:70000", "fe80::1:2", "[::1", "[::1]x", "http://h"} {
		if _, err := ParseAddr(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
