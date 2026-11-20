// Package client is a thin HTTP wrapper over the Plane REST API (v1 and v2).
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/config"
)

// Client sends requests with the X-Api-Key header.
type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	Verbose    bool

	projectIDs map[string]string
}

// New builds a Client from resolved config.
func New(cfg *config.Config) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 120 * time.Second},
		baseURL:    cfg.BaseURL(),
		apiKey:     cfg.APIKey,
		projectIDs: map[string]string{},
	}
}

// Request is one API call. Path starts with /api/.
type Request struct {
	Method string
	Path   string
	Query  url.Values
	Body   any
}

// Response is a raw API response.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

const maxRetries = 3

// Do sends the request. It retries on 429 and honors Retry-After.
func (c *Client) Do(ctx context.Context, r Request) (*Response, error) {
	var payload []byte
	if r.Body != nil {
		b, err := json.Marshal(r.Body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		payload = b
	}
	u := c.baseURL + r.Path
	if len(r.Query) > 0 {
		u += "?" + r.Query.Encode()
	}
	for attempt := 0; ; attempt++ {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, r.Method, u, body)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("X-Api-Key", c.apiKey)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.Verbose {
			fmt.Fprintf(os.Stderr, "→ %s %s\n", r.Method, u)
			if payload != nil && os.Getenv("PLANE_DUMP_BODY") == "1" {
				fmt.Fprintf(os.Stderr, "→ body: %s\n", payload)
			}
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read response body: %w", err)
		}
		if c.Verbose {
			fmt.Fprintf(os.Stderr, "← %s (%d bytes)\n", resp.Status, len(data))
			if os.Getenv("PLANE_DUMP_BODY") == "1" {
				fmt.Fprintf(os.Stderr, "%s\n", data)
			}
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxRetries {
			wait := retryAfter(resp.Header.Get("Retry-After"), attempt)
			if c.Verbose {
				fmt.Fprintf(os.Stderr, "rate limited, retry in %s\n", wait)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		if resp.StatusCode >= 400 {
			return nil, newAPIError(resp.StatusCode, data)
		}
		return &Response{Status: resp.StatusCode, Header: resp.Header, Body: data}, nil
	}
}

func retryAfter(h string, attempt int) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && s >= 0 {
		return min(time.Duration(s)*time.Second, time.Minute)
	}
	return time.Duration(attempt+1) * 2 * time.Second
}

// GetJSON sends a GET and decodes the body into v.
func (c *Client) GetJSON(ctx context.Context, path string, q url.Values, v any) error {
	resp, err := c.Do(ctx, Request{Method: http.MethodGet, Path: path, Query: q})
	if err != nil {
		return err
	}
	return json.Unmarshal(resp.Body, v)
}

// FieldError is one entry of a v2 validation error.
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// APIError is returned for HTTP status >= 400. v2 bodies are RFC 9457
// problem+json; v1 bodies are free-form JSON.
type APIError struct {
	StatusCode int
	Type       string       `json:"type"`
	Code       string       `json:"code"`
	Detail     string       `json:"detail"`
	Errors     []FieldError `json:"errors"`
	Body       []byte       `json:"-"`
}

func newAPIError(status int, body []byte) *APIError {
	e := &APIError{StatusCode: status, Body: body}
	_ = json.Unmarshal(body, e)
	return e
}

func (e *APIError) Error() string {
	if e.Code == "" && e.Detail == "" {
		return fmt.Sprintf("API error (status %d): %s", e.StatusCode, strings.TrimSpace(string(e.Body)))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "API error (status %d, %s): %s", e.StatusCode, e.Code, e.Detail)
	for _, fe := range e.Errors {
		fmt.Fprintf(&b, "\n  %s: %s (%s)", fe.Field, fe.Message, fe.Code)
	}
	return b.String()
}
