package mail

import (
	"strings"
	"testing"
)

func TestSnippetOfCutHTML(t *testing.T) {
	raw := "From: a@example.com\r\nSubject: News\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		"<html><head><style>" + strings.Repeat(".x{color:red}", 500) + "</style></head>" +
		"<body><p>Hello&nbsp;Ann,</p><p>The new   episodes are here.</p>" + strings.Repeat("<p>More text.</p>", 500) +
		"\r\n--b\r\nContent-Type: image/png\r\n"
	cut := []byte(raw[:len(raw)-6]) // the fetch ends inside the header of the next part
	if got := snippet(cut, 40); got != "Hello Ann, The new episodes are here. Mo…" {
		t.Fatalf("snippet = %q", got)
	}
}
