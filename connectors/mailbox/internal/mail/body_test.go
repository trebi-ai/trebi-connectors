package mail

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEventBody(t *testing.T) {
	const head = "From: a@example.com\r\nSubject: Hi\r\nMIME-Version: 1.0\r\n"
	alt := func(parts ...string) string {
		s := head + "Content-Type: multipart/alternative; boundary=b\r\n\r\n"
		for _, p := range parts {
			s += "--b\r\n" + p + "\r\n"
		}
		return s + "--b--\r\n"
	}
	const plainPart = "Content-Type: text/plain; charset=utf-8\r\n\r\nHello Ann.\r\nSee you."
	const htmlPart = "Content-Type: text/html; charset=utf-8\r\n\r\n<p>Hello <b>Bob</b>.</p>"
	long := strings.Repeat("é", maxBody) // 2 bytes each
	for _, tc := range []struct {
		name      string
		raw       string
		whole     bool
		want      string
		truncated bool
	}{
		{"text part only", head + "Content-Type: text/plain\r\n\r\nHello Ann.\r\nSee you.\r\n", true, "Hello Ann.\r\nSee you.", false},
		{"html part only", alt(htmlPart), true, "Hello Bob.", false},
		{"text and html", alt(plainPart, htmlPart), true, "Hello Ann.\r\nSee you.", false},
		{"over 32 KB", head + "Content-Type: text/plain; charset=utf-8\r\n\r\nx" + long + "\r\n", true, "x" + long[:maxBody-2], true},
		{"cut fetch, text part done", alt(plainPart, htmlPart)[:len(alt(plainPart, htmlPart))-20], false, "Hello Ann.\r\nSee you.", false},
		{"cut fetch, inside the text part", head + "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\n" + plainPart, false, "Hello Ann.\r\nSee you.", true},
		{"cut fetch, one part", head + "Content-Type: text/plain\r\n\r\nHello", false, "Hello", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := parse([]byte(tc.raw)) //nolint:errcheck // cut cases end with an error
			got, truncated := m.body(tc.whole)
			if got != tc.want || truncated != tc.truncated {
				t.Fatalf("body = %q (%d bytes), truncated %v; want %q, %v", got[:min(len(got), 80)], len(got), truncated, tc.want[:min(len(tc.want), 80)], tc.truncated)
			}
			if len(got) > maxBody || !utf8.ValidString(got) {
				t.Fatalf("body has %d bytes, valid UTF-8 %v", len(got), utf8.ValidString(got))
			}
		})
	}
}
