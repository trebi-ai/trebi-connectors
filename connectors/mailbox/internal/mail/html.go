package mail

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var (
	blankLines = regexp.MustCompile(`\n{3,}`)
	spaceRuns  = regexp.MustCompile(`[ \t\f\r]+`)
)

// blockTags start a new line.
var blockTags = map[string]bool{
	"p": true, "div": true, "br": true, "tr": true, "li": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "blockquote": true, "pre": true, "table": true, "ul": true, "ol": true, "hr": true,
}

// HTMLText converts HTML to plain text. It drops scripts, styles, and
// images, and keeps the target of each link.
func HTMLText(src string) string {
	z := html.NewTokenizer(strings.NewReader(src))
	var b strings.Builder
	skip, cells := 0, 0
	var href string
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return tidy(b.String())
		case html.TextToken:
			if skip == 0 {
				b.WriteString(spaceRuns.ReplaceAllString(strings.ReplaceAll(string(z.Text()), "\n", " "), " "))
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			switch tag {
			case "script", "style", "head", "title":
				if tt == html.StartTagToken {
					skip++
				}
			case "a":
				href = ""
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					if string(k) == "href" {
						href = string(v)
					}
				}
			case "li":
				b.WriteString("\n- ")
				continue
			case "tr":
				cells = 0
			case "td", "th":
				if cells > 0 {
					b.WriteString(" | ")
				}
				cells++
			}
			if blockTags[tag] {
				b.WriteString("\n")
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			switch tag {
			case "script", "style", "head", "title":
				if skip > 0 {
					skip--
				}
			case "a":
				if strings.HasPrefix(href, "http") {
					b.WriteString(" (" + href + ")")
				}
				href = ""
			case "li", "tr": // the next item or row starts its own line
				continue
			}
			if blockTags[tag] {
				b.WriteString("\n")
			}
		}
	}
}

func tidy(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// attribution is the line before a quote, for example "On Mon, Ann
// <ann@example.com> wrote:".
var attribution = regexp.MustCompile(`(?i)^(on\b.*|.*\bwrote|.*\bescreveu|.*\bschrieb|.*\ba écrit)\s*:?\s*$`)

// StripQuotes removes the quoted lines (">") and the attribution line
// before them. It keeps the text when all lines are quoted.
func StripQuotes(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimLeft(l, " "), ">") {
			for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
				out = out[:len(out)-1]
			}
			if n := len(out); n > 0 && attribution.MatchString(strings.TrimSpace(out[n-1])) {
				out = out[:n-1]
				// Gmail wraps a long attribution over two lines.
				if n := len(out); n > 0 && strings.HasPrefix(strings.ToLower(strings.TrimSpace(out[n-1])), "on ") && !strings.HasSuffix(strings.TrimSpace(out[n-1]), ".") {
					out = out[:n-1]
				}
			}
			continue
		}
		out = append(out, l)
	}
	s := strings.TrimSpace(strings.Join(out, "\n"))
	if s == "" {
		return strings.TrimSpace(text)
	}
	return s
}

// StripHTMLQuotes removes the Gmail quote blocks (class gmail_quote) and
// <blockquote type="cite"> from HTML.
func StripHTMLQuotes(src string) string {
	z := html.NewTokenizer(strings.NewReader(src))
	var b strings.Builder
	depth := 0 // > 0 inside a quote
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return b.String()
		}
		raw := string(z.Raw())
		switch tt {
		case html.StartTagToken:
			name, hasAttr := z.TagName()
			if depth > 0 {
				if !voidTags[string(name)] {
					depth++
				}
				continue
			}
			quote := false
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				switch {
				case string(k) == "class" && strings.Contains(string(v), "gmail_quote"):
					quote = true
				case string(name) == "blockquote" && string(k) == "type" && string(v) == "cite":
					quote = true
				}
			}
			if quote {
				depth = 1
				continue
			}
		case html.EndTagToken:
			if depth > 0 {
				depth--
				continue
			}
		default:
			if depth > 0 {
				continue
			}
		}
		b.WriteString(raw)
	}
}

var voidTags = map[string]bool{"br": true, "img": true, "hr": true, "meta": true, "input": true, "link": true, "wbr": true, "col": true, "area": true, "base": true, "source": true}
