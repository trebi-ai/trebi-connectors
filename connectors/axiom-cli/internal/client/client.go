package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const userAgent = "axiom-cli/0.1.0"

// Client is a thin wrapper around http.Client that handles auth, org header,
// JSON marshalling and error parsing for the Axiom REST API.
type Client struct {
	BaseURL    string
	Token      string
	OrgID      string
	HTTPClient *http.Client
}

// New constructs a Client with sane defaults.
func New(baseURL, token, orgID string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Token:      token,
		OrgID:      orgID,
		HTTPClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// Options carries optional knobs for a request.
type Options struct {
	Query       url.Values
	Body        any       // marshalled as JSON when non-nil
	BodyRaw     io.Reader // overrides Body if set
	ContentType string    // overrides default application/json when BodyRaw is set
	Headers     map[string]string
}

// APIError is returned for any non-2xx response.
type APIError struct {
	Status  int
	Message string
	Body    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("axiom: %d %s: %s", e.Status, http.StatusText(e.Status), e.Message)
	}
	if e.Body != "" {
		return fmt.Sprintf("axiom: %d %s: %s", e.Status, http.StatusText(e.Status), e.Body)
	}
	return fmt.Sprintf("axiom: %d %s", e.Status, http.StatusText(e.Status))
}

// Do issues a single HTTP request and returns the raw response body.
func (c *Client) Do(ctx context.Context, method, path string, opts *Options) (json.RawMessage, error) {
	if opts == nil {
		opts = &Options{}
	}

	u := c.BaseURL + path
	if len(opts.Query) > 0 {
		u += "?" + opts.Query.Encode()
	}

	var body io.Reader
	contentType := opts.ContentType
	switch {
	case opts.BodyRaw != nil:
		body = opts.BodyRaw
		if contentType == "" {
			contentType = "application/octet-stream"
		}
	case opts.Body != nil:
		switch v := opts.Body.(type) {
		case json.RawMessage:
			body = bytes.NewReader(v)
		case []byte:
			body = bytes.NewReader(v)
		default:
			b, err := json.Marshal(opts.Body)
			if err != nil {
				return nil, fmt.Errorf("marshal body: %w", err)
			}
			body = bytes.NewReader(b)
		}
		if contentType == "" {
			contentType = "application/json"
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.OrgID != "" {
		req.Header.Set("X-Axiom-Org-ID", c.OrgID)
	}
	if contentType != "" && body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range opts.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{Status: resp.StatusCode, Body: truncate(string(respBody), 1000)}
		var parsed struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal(respBody, &parsed); err == nil {
			if parsed.Message != "" {
				apiErr.Message = parsed.Message
			} else if parsed.Error != "" {
				apiErr.Message = parsed.Error
			}
		}
		return nil, apiErr
	}

	if len(respBody) == 0 {
		return json.RawMessage("null"), nil
	}
	return json.RawMessage(respBody), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Get issues a GET request.
func (c *Client) Get(ctx context.Context, path string, q url.Values) (json.RawMessage, error) {
	return c.Do(ctx, http.MethodGet, path, &Options{Query: q})
}

// Post issues a POST with a JSON body.
func (c *Client) Post(ctx context.Context, path string, body any) (json.RawMessage, error) {
	return c.Do(ctx, http.MethodPost, path, &Options{Body: body})
}

// PostQuery issues a POST with a JSON body and query parameters.
func (c *Client) PostQuery(ctx context.Context, path string, q url.Values, body any) (json.RawMessage, error) {
	return c.Do(ctx, http.MethodPost, path, &Options{Body: body, Query: q})
}

// Put issues a PUT with a JSON body.
func (c *Client) Put(ctx context.Context, path string, body any) (json.RawMessage, error) {
	return c.Do(ctx, http.MethodPut, path, &Options{Body: body})
}

// Delete issues a DELETE request.
func (c *Client) Delete(ctx context.Context, path string) (json.RawMessage, error) {
	return c.Do(ctx, http.MethodDelete, path, nil)
}

// Stream sends an arbitrary body (used for ingest where the body may be large
// NDJSON / CSV / JSON). The reader is consumed without buffering.
func (c *Client) Stream(ctx context.Context, method, path, contentType string, body io.Reader, q url.Values) (json.RawMessage, error) {
	return c.Do(ctx, method, path, &Options{BodyRaw: body, ContentType: contentType, Query: q})
}
