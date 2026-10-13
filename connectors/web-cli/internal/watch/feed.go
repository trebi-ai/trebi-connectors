// Package watch reads web addresses: RSS and Atom feeds as entries, and
// HTML pages as text.
package watch

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"time"
)

// Feed is one parsed RSS or Atom feed.
type Feed struct {
	Title   string
	Link    string
	Entries []Entry
}

// Entry is one item of a feed.
type Entry struct {
	ID        string `json:"entry_id"`
	Title     string `json:"title,omitempty"`
	Link      string `json:"link,omitempty"`
	Author    string `json:"author,omitempty"`
	Published string `json:"published,omitempty"` // RFC 3339, or "" when the feed gives no valid date
	Summary   string `json:"summary,omitempty"`
}

// ErrNotFeed means that the body is not RSS or Atom.
var ErrNotFeed = errors.New("not an RSS or Atom feed")

// maxSummary caps the summary text of one entry.
const maxSummary = 2000

type rssDoc struct {
	Channel struct {
		Title string    `xml:"title"`
		Link  string    `xml:"link"`
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
	Items []rssItem `xml:"item"` // RSS 1.0 (RDF) puts the items at the root
}

type rssItem struct {
	GUID        string `xml:"guid"`
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	PubDate     string `xml:"pubDate"`
	Date        string `xml:"http://purl.org/dc/elements/1.1/ date"`
	Author      string `xml:"author"`
	Creator     string `xml:"http://purl.org/dc/elements/1.1/ creator"`
	Description string `xml:"description"`
	About       string `xml:"about,attr"`
}

type atomDoc struct {
	Title   string      `xml:"title"`
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

type atomEntry struct {
	ID        string     `xml:"id"`
	Title     string     `xml:"title"`
	Links     []atomLink `xml:"link"`
	Updated   string     `xml:"updated"`
	Published string     `xml:"published"`
	Summary   string     `xml:"summary"`
	Content   string     `xml:"content"`
	Author    struct {
		Name string `xml:"name"`
	} `xml:"author"`
}

// ParseFeed reads an RSS 2.0, RSS 1.0, or Atom document. It returns
// ErrNotFeed for another document.
func ParseFeed(body []byte) (Feed, error) {
	root, err := rootName(body)
	if err != nil {
		return Feed{}, err
	}
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	switch root {
	case "rss", "RDF":
		var d rssDoc
		if err := dec.Decode(&d); err != nil {
			return Feed{}, err
		}
		f := Feed{Title: clean(d.Channel.Title), Link: strings.TrimSpace(d.Channel.Link)}
		for _, it := range append(d.Channel.Items, d.Items...) {
			e := Entry{
				ID:        firstOf(it.GUID, it.About, it.Link),
				Title:     clean(it.Title),
				Link:      strings.TrimSpace(it.Link),
				Author:    clean(firstOf(it.Creator, it.Author)),
				Published: parseDate(firstOf(it.PubDate, it.Date)),
				Summary:   summary(it.Description),
			}
			f.Entries = append(f.Entries, withID(e))
		}
		return f, nil
	case "feed":
		var d atomDoc
		if err := dec.Decode(&d); err != nil {
			return Feed{}, err
		}
		f := Feed{Title: clean(d.Title), Link: altLink(d.Links)}
		for _, it := range d.Entries {
			e := Entry{
				ID:        strings.TrimSpace(it.ID),
				Title:     clean(it.Title),
				Link:      altLink(it.Links),
				Author:    clean(it.Author.Name),
				Published: parseDate(firstOf(it.Published, it.Updated)),
				Summary:   summary(firstOf(it.Summary, it.Content)),
			}
			f.Entries = append(f.Entries, withID(e))
		}
		return f, nil
	}
	return Feed{}, ErrNotFeed
}

// rootName returns the local name of the first element, or ErrNotFeed when
// the body is not XML.
func rootName(body []byte) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", ErrNotFeed
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local, nil
		}
	}
}

// withID gives an entry with no id the link, or the title and the date.
func withID(e Entry) Entry {
	if e.ID == "" {
		e.ID = firstOf(e.Link, e.Title+"|"+e.Published)
	}
	return e
}

func altLink(ls []atomLink) string {
	for _, l := range ls {
		if l.Rel == "" || l.Rel == "alternate" {
			return strings.TrimSpace(l.Href)
		}
	}
	if len(ls) > 0 {
		return strings.TrimSpace(ls[0].Href)
	}
	return ""
}

var dateLayouts = []string{
	time.RFC3339Nano, time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822,
	"Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST", "2 Jan 2006 15:04:05 -0700",
	"2006-01-02T15:04:05", "2006-01-02",
}

func parseDate(v string) string {
	v = strings.TrimSpace(v)
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, v); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

// summary turns an HTML or text description into short plain text.
func summary(v string) string {
	s := clean(HTMLText(v))
	if len(s) > maxSummary {
		s = strings.ToValidUTF8(s[:maxSummary], "") + "…"
	}
	return s
}

func clean(v string) string { return strings.Join(strings.Fields(v), " ") }

func firstOf(vs ...string) string {
	for _, v := range vs {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
