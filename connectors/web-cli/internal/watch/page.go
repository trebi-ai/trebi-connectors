package watch

import (
	"fmt"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Page is the text of one HTML page.
type Page struct {
	Title string
	Text  string
	// Matches is the count of elements that the selector found. It is 1
	// without a selector.
	Matches int
}

// ParsePage reads an HTML page. With a selector, Text is the text of each
// matching element, one per line. Without one, Text is the text of the
// body. Scripts, styles, and templates give no text.
func ParsePage(body []byte, selector string) (Page, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return Page{}, err
	}
	p := Page{Title: clean(text(cascadia.Query(doc, cascadia.MustCompile("title"))))}
	if selector == "" {
		root := cascadia.Query(doc, cascadia.MustCompile("body"))
		if root == nil {
			root = doc
		}
		p.Text, p.Matches = text(root), 1
		return p, nil
	}
	sel, err := cascadia.Compile(selector)
	if err != nil {
		return Page{}, fmt.Errorf("selector %q: %w", selector, err)
	}
	nodes := cascadia.QueryAll(doc, sel)
	parts := make([]string, 0, len(nodes))
	for _, n := range nodes {
		parts = append(parts, text(n))
	}
	p.Text, p.Matches = strings.Join(parts, "\n"), len(nodes)
	return p, nil
}

// HTMLText returns the text of an HTML fragment. Plain text stays as it is.
func HTMLText(v string) string {
	if !strings.ContainsAny(v, "<&") {
		return v
	}
	nodes, err := html.ParseFragment(strings.NewReader(v), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return v
	}
	var b strings.Builder
	for _, n := range nodes {
		if n.Type == html.ElementNode && skip[n.DataAtom] {
			continue
		}
		b.WriteString(text(n))
		b.WriteByte('\n')
	}
	return b.String()
}

var skip = map[atom.Atom]bool{atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true, atom.Head: true}

var block = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Br: true, atom.Li: true, atom.Tr: true, atom.H1: true, atom.H2: true,
	atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true, atom.Section: true, atom.Article: true,
	atom.Header: true, atom.Footer: true, atom.Ul: true, atom.Ol: true, atom.Table: true, atom.Pre: true,
	atom.Blockquote: true, atom.Td: true, atom.Th: true, atom.Dt: true, atom.Dd: true, atom.Hr: true,
}

// text returns the visible text of n: one line for each block, with the
// spaces in a line made single, and no empty lines.
func text(root *html.Node) string {
	if root == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			b.WriteString(n.Data)
			return
		case html.ElementNode:
			if skip[n.DataAtom] && n != root {
				return
			}
			if block[n.DataAtom] {
				b.WriteByte('\n')
				defer b.WriteByte('\n')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	var lines []string
	for l := range strings.SplitSeq(b.String(), "\n") {
		if l = clean(l); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}
