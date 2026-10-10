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
	skip := 0
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
