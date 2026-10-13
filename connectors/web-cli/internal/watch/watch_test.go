package watch_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/trebi-ai/trebi-connectors/connectors/web-cli/internal/fakeweb"
	"github.com/trebi-ai/trebi-connectors/connectors/web-cli/internal/watch"
)

func TestParseRSS(t *testing.T) {
	f, err := watch.ParseFeed([]byte(`<?xml version="1.0" encoding="ISO-8859-1"?>
<rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/"><channel><title> My  blog </title><link>https://b.example/</link>
<item><guid isPermaLink="false">id-2</guid><title>Two</title><link>https://b.example/2</link><pubDate>Thu, 01 Oct 2026 10:00:00 +0200</pubDate><dc:creator>Ann</dc:creator><description><![CDATA[<p>Hello <b>world</b></p><script>x()</script>]]></description></item>
<item><title>One</title><link>https://b.example/1</link></item>
</channel></rss>`))
	if err != nil {
		t.Fatal(err)
	}
	if f.Title != "My blog" || len(f.Entries) != 2 {
		t.Fatalf("%+v", f)
	}
	e := f.Entries[0]
	if e.ID != "id-2" || e.Published != "2026-10-01T08:00:00Z" || e.Author != "Ann" || e.Summary != "Hello world" {
		t.Fatalf("%+v", e)
	}
	if f.Entries[1].ID != "https://b.example/1" {
		t.Fatalf("an entry with no guid must use the link: %+v", f.Entries[1])
	}
}

func TestParseAtomAndRDF(t *testing.T) {
	f, err := watch.ParseFeed([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"><title>News</title>
<entry><id>urn:1</id><title>A</title><link rel="self" href="https://n.example/self"/><link rel="alternate" href="https://n.example/a"/><updated>2026-10-02T00:00:00Z</updated><author><name>Bo</name></author><content type="html">&lt;i&gt;Body&lt;/i&gt;</content></entry></feed>`))
	if err != nil {
		t.Fatal(err)
	}
	if e := f.Entries[0]; f.Title != "News" || e.ID != "urn:1" || e.Link != "https://n.example/a" || e.Author != "Bo" || e.Summary != "Body" || e.Published != "2026-10-02T00:00:00Z" {
		t.Fatalf("%+v", f)
	}
	f, err = watch.ParseFeed([]byte(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/"><channel><title>Old</title></channel><item rdf:about="https://o.example/1"><title>X</title><link>https://o.example/1</link></item></rdf:RDF>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 1 || f.Entries[0].ID != "https://o.example/1" {
		t.Fatalf("%+v", f)
	}
}

func TestParseFeedNotFeed(t *testing.T) {
	for _, b := range []string{`<!doctype html><html><body>x</body></html>`, `{"a":1}`, ``} {
		if _, err := watch.ParseFeed([]byte(b)); !errors.Is(err, watch.ErrNotFeed) {
			t.Fatalf("%q: %v", b, err)
		}
	}
}

func TestParsePage(t *testing.T) {
	body := []byte(`<html><head><title>Shop</title><style>.x{}</style></head><body><h1>Lamp</h1><script>var a</script><p>Nice   lamp.</p><ul><li class="p">10 EUR</li><li class="p">12 EUR</li></ul></body></html>`)
	p, err := watch.ParsePage(body, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Shop" || p.Text != "Lamp\nNice lamp.\n10 EUR\n12 EUR" {
		t.Fatalf("%+v", p)
	}
	p, err = watch.ParsePage(body, "li.p")
	if err != nil {
		t.Fatal(err)
	}
	if p.Text != "10 EUR\n12 EUR" || p.Matches != 2 {
		t.Fatalf("%+v", p)
	}
	if _, err := watch.ParsePage(body, "li[["); err == nil {
		t.Fatal("want a selector error")
	}
}

func TestFetcherRead(t *testing.T) {
	fake := fakeweb.Start()
	t.Cleanup(fake.Close)
	f := watch.NewFetcher(nil, "test")
	ctx := context.Background()
	r, err := f.Read(ctx, fake.URL(fakeweb.RSSPath), "")
	if err != nil || r.Feed == nil || r.Feed.Entries[0].ID != "e1" {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = f.Read(ctx, fake.URL(fakeweb.AtomPath), "")
	if err != nil || r.Feed == nil || r.Feed.Entries[0].ID != "urn:fake:e1" {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = f.Read(ctx, fake.URL(fakeweb.PagePath), "#price")
	if err != nil || r.Page == nil || r.Page.Text != "Price: 10 EUR" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := f.Read(ctx, fake.URL("/nothing"), ""); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("got %v", err)
	}
}
