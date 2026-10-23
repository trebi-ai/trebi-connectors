package vnc

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Addr is a parsed server address.
type Addr struct {
	Network string // tcp, unix, ws, or wss
	Address string // host:port, a socket path, or a URL
}

func (a Addr) String() string { return a.Address }

// Host is the host name for TLS checks, or "" for a unix socket.
func (a Addr) Host() string {
	if a.Network != "tcp" {
		return ""
	}
	h, _, _ := net.SplitHostPort(a.Address)
	return h
}

// ParseAddr reads the vncdotool address forms:
//
//	host             display 0, port 5900
//	host:N           display N when N < 100, else port N (TigerVNC rule)
//	host::PORT       port PORT
//	[v6], [v6]:N, [v6]::PORT
//	/path, unix:/path  a unix socket
func ParseAddr(s string) (Addr, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return Addr{}, fmt.Errorf("address is empty")
	case strings.HasPrefix(s, "unix:"):
		return Addr{"unix", strings.TrimPrefix(s, "unix:")}, nil
	case strings.HasPrefix(s, "/"), strings.HasPrefix(s, "./"):
		return Addr{"unix", s}, nil
	case strings.HasPrefix(s, "ws://"), strings.HasPrefix(s, "wss://"):
		return Addr{strings.SplitN(s, ":", 2)[0], s}, nil
	case strings.Contains(s, "://"):
		return Addr{}, fmt.Errorf("address %q: unknown scheme", s)
	}

	host, rest := s, ""
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return Addr{}, fmt.Errorf("address %q: no closing ]", s)
		}
		host, rest = s[1:end], s[end+1:]
		if rest != "" && !strings.HasPrefix(rest, ":") {
			return Addr{}, fmt.Errorf("address %q: text after ]", s)
		}
	} else if i := strings.Index(s, ":"); i >= 0 {
		if strings.Count(s, ":") > 1 && !strings.Contains(s, "::") {
			return Addr{}, fmt.Errorf("address %q: put an IPv6 address in brackets", s)
		}
		host, rest = s[:i], s[i:]
	}
	if host == "" {
		host = "localhost"
	}

	port := 5900
	switch {
	case rest == "":
	case strings.HasPrefix(rest, "::"):
		n, err := strconv.Atoi(rest[2:])
		if err != nil || n <= 0 || n > 65535 {
			return Addr{}, fmt.Errorf("address %q: bad port", s)
		}
		port = n
	default:
		n, err := strconv.Atoi(rest[1:])
		if err != nil || n < 0 || n > 65535 {
			return Addr{}, fmt.Errorf("address %q: bad display or port", s)
		}
		if n < 100 {
			port = 5900 + n
		} else {
			port = n
		}
	}
	return Addr{"tcp", net.JoinHostPort(host, strconv.Itoa(port))}, nil
}
