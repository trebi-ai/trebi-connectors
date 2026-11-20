// Package markdown converts GitHub Flavored Markdown to the HTML that Plane stores.
package markdown

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// ParseError is a Markdown problem that stops the conversion. Line is 1-based (0 = whole input).
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string {
	if e.Line == 0 {
		return "markdown: " + e.Msg
	}
	return fmt.Sprintf("markdown: line %d: %s", e.Line, e.Msg)
}

// Converter renders GFM (tables, strikethrough, autolinks, task lists) to HTML.
// Task lists use the Plane editor form (see planeTaskList).
type Converter struct {
	md goldmark.Markdown
}

// New returns a Converter. Raw HTML in the source passes through; Plane sanitizes it.
func New() *Converter {
	return &Converter{md: goldmark.New(
		goldmark.WithExtensions(extension.GFM, planeTaskList{}),
		goldmark.WithRendererOptions(html.WithUnsafe()),
	)}
}

// HTML checks the source with Validate, then converts it to HTML.
func (c *Converter) HTML(src []byte) (string, error) {
	if err := Validate(src); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := c.md.Convert(src, &buf); err != nil {
		return "", fmt.Errorf("markdown: %w", err)
	}
	out := blockTagNewline.ReplaceAllString(buf.String(), "$1")
	return strings.ReplaceAll(out, "\n</code></pre>", "</code></pre>"), nil
}

// blockTagNewline matches the newline after a block tag. The Plane editor reads
// it as one more node (empty list items, table cells, and paragraphs).
// A newline after an inline tag is a soft line break and stays.
var blockTagNewline = regexp.MustCompile(`(</?(?:p|h[1-6]|ul|ol|li|blockquote|pre|table|thead|tbody|tr|th|td)(?:\s[^>]*)?>|<hr>)\n`)

// Validate rejects input that CommonMark accepts but that is almost always a
// mistake: empty or binary input, and a code fence that is not closed (it
// silently turns the rest of the document into code).
func Validate(src []byte) error {
	if len(bytes.TrimSpace(src)) == 0 {
		return &ParseError{Msg: "input is empty"}
	}
	if !utf8.Valid(src) {
		return &ParseError{Line: invalidUTF8Line(src), Msg: "input is not valid UTF-8"}
	}
	if i := bytes.IndexByte(src, 0); i >= 0 {
		return &ParseError{Line: bytes.Count(src[:i], []byte("\n")) + 1, Msg: "input contains a NUL byte (binary file?)"}
	}
	return checkFences(src)
}

func invalidUTF8Line(src []byte) int {
	line := 1
	for len(src) > 0 {
		r, size := utf8.DecodeRune(src)
		if r == utf8.RuneError && size <= 1 {
			return line
		}
		if r == '\n' {
			line++
		}
		src = src[size:]
	}
	return line
}

// containerPrefix matches blockquote markers and list markers before a line's content.
var containerPrefix = regexp.MustCompile(`^(?:[ \t]*(?:>|[-*+][ \t]|\d{1,9}[.)][ \t]))*[ \t]*`)

// checkFences finds a fenced code block without a closing fence.
// Fences inside blockquotes and list items are matched after their markers.
func checkFences(src []byte) error {
	var open struct {
		char  byte
		size  int
		line  int
		inUse bool
	}
	for i, raw := range strings.Split(string(src), "\n") {
		line := containerPrefix.ReplaceAllString(raw, "")
		char, size := fence(line)
		if size == 0 {
			continue
		}
		if !open.inUse {
			// An opening backtick fence cannot have a backtick in its info string.
			if char == '`' && strings.ContainsRune(line[size:], '`') {
				continue
			}
			open.char, open.size, open.line, open.inUse = char, size, i+1, true
			continue
		}
		if char == open.char && size >= open.size && strings.TrimSpace(line[size:]) == "" {
			open.inUse = false
		}
	}
	if open.inUse {
		return &ParseError{Line: open.line, Msg: fmt.Sprintf("code fence %q is not closed", strings.Repeat(string(open.char), open.size))}
	}
	return nil
}

// fence returns the fence character and run length when line starts with ``` or ~~~.
func fence(line string) (byte, int) {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return 0, 0
	}
	n := 0
	for n < len(line) && line[n] == line[0] {
		n++
	}
	if n < 3 {
		return 0, 0
	}
	return line[0], n
}
