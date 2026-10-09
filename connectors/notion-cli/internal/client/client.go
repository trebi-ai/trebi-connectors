// Package client is a small Notion REST client.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the Notion API.
const DefaultBaseURL = "https://api.notion.com/v1"

// Version is the Notion-Version header.
const Version = "2022-06-28"

// Client calls the Notion API with one integration secret.
type Client struct {
	BaseURL string
	token   string
	http    *http.Client
}

// New builds a client.
func New(token string) *Client {
	return &Client{BaseURL: DefaultBaseURL, token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

// HasToken reports whether the client has a secret.
func (c *Client) HasToken() bool { return c.token != "" }

// APIError is a Notion error response.
type APIError struct {
	Status  int    `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("notion: %d %s: %s", e.Status, e.Code, e.Message)
}

// DoJSON sends one request. in is the JSON body or nil; out gets the body.
func (c *Client) DoJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Notion-Version", Version)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // read only
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		e := &APIError{Status: resp.StatusCode}
		_ = json.Unmarshal(data, e) //nolint:errcheck // the status is enough without a body
		e.Status = resp.StatusCode
		return e
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// User is a Notion user or bot.
type User struct {
	Object string `json:"object,omitempty"`
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Type   string `json:"type,omitempty"`
}

// RichText is one run of text.
type RichText struct {
	PlainText string `json:"plain_text"`
}

// Parent is where a page or a database is.
type Parent struct {
	Type       string `json:"type"`
	DatabaseID string `json:"database_id,omitempty"`
	PageID     string `json:"page_id,omitempty"`
	Workspace  bool   `json:"workspace,omitempty"`
}

// Property is one page property. Only the title is read.
type Property struct {
	Type  string     `json:"type"`
	Title []RichText `json:"title,omitempty"`
}

// Page is a Notion page.
type Page struct {
	Object         string              `json:"object"`
	ID             string              `json:"id"`
	CreatedTime    string              `json:"created_time,omitempty"`
	LastEditedTime string              `json:"last_edited_time,omitempty"`
	LastEditedBy   *User               `json:"last_edited_by,omitempty"`
	URL            string              `json:"url,omitempty"`
	Archived       bool                `json:"archived,omitempty"`
	Parent         Parent              `json:"parent"`
	Properties     map[string]Property `json:"properties,omitempty"`
}

// Title returns the plain text of the title property.
func (p Page) Title() string {
	for _, prop := range p.Properties {
		if prop.Type == "title" {
			return plain(prop.Title)
		}
	}
	return ""
}

// Database is a Notion database.
type Database struct {
	Object         string     `json:"object"`
	ID             string     `json:"id"`
	LastEditedTime string     `json:"last_edited_time,omitempty"`
	URL            string     `json:"url,omitempty"`
	Title          []RichText `json:"title,omitempty"`
	Parent         Parent     `json:"parent"`
}

// Name returns the plain text of the title.
func (d Database) Name() string { return plain(d.Title) }

func plain(rt []RichText) string {
	var b strings.Builder
	for _, r := range rt {
		b.WriteString(r.PlainText)
	}
	return b.String()
}

// SearchParams is the body of POST /search.
type SearchParams struct {
	Query       string        `json:"query,omitempty"`
	Filter      *SearchFilter `json:"filter,omitempty"`
	Sort        *SearchSort   `json:"sort,omitempty"`
	StartCursor string        `json:"start_cursor,omitempty"`
	PageSize    int           `json:"page_size,omitempty"`
}

// SearchFilter limits a search to pages or databases.
type SearchFilter struct {
	Property string `json:"property"`
	Value    string `json:"value"`
}

// SearchSort orders a search.
type SearchSort struct {
	Direction string `json:"direction"`
	Timestamp string `json:"timestamp"`
}

// SearchResult is one page of results.
type SearchResult struct {
	Results    []Page `json:"results"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

// Me returns the bot user of the secret.
func (c *Client) Me(ctx context.Context) (User, error) {
	var u User
	err := c.DoJSON(ctx, http.MethodGet, "/users/me", nil, &u)
	return u, err
}

// Page reads one page.
func (c *Client) Page(ctx context.Context, id string) (Page, error) {
	var p Page
	err := c.DoJSON(ctx, http.MethodGet, "/pages/"+id, nil, &p)
	return p, err
}

// Database reads one database.
func (c *Client) Database(ctx context.Context, id string) (Database, error) {
	var d Database
	err := c.DoJSON(ctx, http.MethodGet, "/databases/"+id, nil, &d)
	return d, err
}

// Search finds pages. Results that are databases have no properties.
func (c *Client) Search(ctx context.Context, p SearchParams) (SearchResult, error) {
	var r SearchResult
	err := c.DoJSON(ctx, http.MethodPost, "/search", p, &r)
	return r, err
}

// EditedPages searches pages, newest edit first.
func (c *Client) EditedPages(ctx context.Context, query, cursor string, size int) (SearchResult, error) {
	return c.Search(ctx, SearchParams{
		Query:       query,
		Filter:      &SearchFilter{Property: "object", Value: "page"},
		Sort:        &SearchSort{Direction: "descending", Timestamp: "last_edited_time"},
		StartCursor: cursor,
		PageSize:    size,
	})
}
