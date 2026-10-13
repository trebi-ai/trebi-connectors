// Package fakeweb is an HTTP server with an RSS feed, an Atom feed, and an
// HTML page that change on request. The tests and `serve --sandbox` use it.
package fakeweb

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Paths of the server.
const (
	RSSPath  = "/feed.xml"
	AtomPath = "/atom.xml"
	PagePath = "/page.html"
)

// Server is a running fake site.
type Server struct {
	srv *httptest.Server

	mu      sync.Mutex
	entries []Entry
	price   string
	auto    bool
	reads   map[string]int
}

// Entry is one entry of both feeds.
type Entry struct {
	ID, Title, Link string
	Published       time.Time
}

// Start runs a server with one entry and the price "10".
func Start() *Server {
	s := &Server{price: "10", reads: map[string]int{}, entries: []Entry{{ID: "e1", Title: "First post", Link: "https://example.com/1", Published: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}}}
	mux := http.NewServeMux()
	mux.HandleFunc(RSSPath, s.rss)
	mux.HandleFunc(AtomPath, s.atom)
	mux.HandleFunc(PagePath, s.page)
	s.srv = httptest.NewServer(mux)
	return s
}

// URL returns the full address of path.
func (s *Server) URL(path string) string { return s.srv.URL + path }

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// Add adds an entry to both feeds.
func (s *Server) Add(e Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
}

// SetPrice changes the text of the #price element.
func (s *Server) SetPrice(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.price = p
}

// Auto makes each read of the RSS feed after the first add one entry, and
// each read of the page change the price (sandbox).
func (s *Server) Auto() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auto = true
}

func (s *Server) snapshot(path string) ([]Entry, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auto {
		return append([]Entry(nil), s.entries...), s.price
	}
	s.reads[path]++
	n := s.reads[path]
	switch {
	case path == RSSPath && n > 1:
		m := len(s.entries) + 1
		s.entries = append(s.entries, Entry{ID: fmt.Sprintf("e%d", m), Title: fmt.Sprintf("Post %d", m), Link: fmt.Sprintf("https://example.com/%d", m), Published: time.Date(2026, 10, 1, 9, 0, m, 0, time.UTC)})
	case path == PagePath:
		s.price = fmt.Sprint(10 + n)
	}
	return append([]Entry(nil), s.entries...), s.price
}

func (s *Server) rss(w http.ResponseWriter, _ *http.Request) {
	entries, _ := s.snapshot(RSSPath)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>Fake blog</title><link>https://example.com/</link>`)
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		fmt.Fprintf(&b, `<item><guid>%s</guid><title>%s</title><link>%s</link><pubDate>%s</pubDate><description>&lt;p&gt;About %s&lt;/p&gt;</description></item>`,
			html.EscapeString(e.ID), html.EscapeString(e.Title), html.EscapeString(e.Link), e.Published.Format(time.RFC1123Z), html.EscapeString(e.Title))
	}
	b.WriteString(`</channel></rss>`)
	w.Header().Set("Content-Type", "application/rss+xml")
	_, _ = w.Write([]byte(b.String())) //nolint:errcheck // test server
}

func (s *Server) atom(w http.ResponseWriter, _ *http.Request) {
	entries, _ := s.snapshot(AtomPath)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Fake news</title><link href="https://example.com/"/>`)
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		fmt.Fprintf(&b, `<entry><id>urn:fake:%s</id><title>%s</title><link rel="alternate" href="%s"/><updated>%s</updated><author><name>Ann</name></author><summary>About %s</summary></entry>`,
			html.EscapeString(e.ID), html.EscapeString(e.Title), html.EscapeString(e.Link), e.Published.Format(time.RFC3339), html.EscapeString(e.Title))
	}
	b.WriteString(`</feed>`)
	w.Header().Set("Content-Type", "application/atom+xml")
	_, _ = w.Write([]byte(b.String())) //nolint:errcheck // test server
}

func (s *Server) page(w http.ResponseWriter, _ *http.Request) {
	_, price := s.snapshot(PagePath)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><title>Fake shop</title><style>p{color:red}</style></head><body><h1>Lamp</h1><script>var x = 1;</script><p>A desk lamp.</p><p id="price">Price: %s EUR</p></body></html>`, html.EscapeString(price))
}
