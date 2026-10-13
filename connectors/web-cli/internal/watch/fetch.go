package watch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// maxBody caps one response.
const maxBody = 10 << 20

// Result is what one address gives: a feed, or else a page.
type Result struct {
	Feed *Feed
	Page *Page
}

// Fetcher reads web addresses.
type Fetcher struct {
	client *http.Client
	agent  string
}

// NewFetcher builds a Fetcher. A nil client uses a client with a 30 s
// timeout.
func NewFetcher(client *http.Client, version string) *Fetcher {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Fetcher{client: client, agent: "web-cli/" + version + " (+https://trebi.ai)"}
}

// Read gets url. A feed body gives Result.Feed. Another body gives
// Result.Page with the text of selector.
func (f *Fetcher) Read(ctx context.Context, url, selector string) (Result, error) {
	body, err := f.get(ctx, url)
	if err != nil {
		return Result{}, err
	}
	feed, err := ParseFeed(body)
	if err == nil {
		return Result{Feed: &feed}, nil
	}
	if !errors.Is(err, ErrNotFeed) {
		return Result{}, fmt.Errorf("%s: %w", url, err)
	}
	page, err := ParsePage(body, selector)
	if err != nil {
		return Result{}, err
	}
	return Result{Page: &page}, nil
}

func (f *Fetcher) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.agent)
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, text/html;q=0.8, */*;q=0.5")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}
