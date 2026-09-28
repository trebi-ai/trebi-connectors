package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"time"
)

const (
	defaultBaseURL = "https://discord.com/api/v10"
	userAgent      = "DiscordBot (https://github.com/trebi-ai/trebi-connectors, 1.0)"
)

// Client is a Discord REST API client.
type Client struct {
	Token      string
	BaseURL    string
	HTTPClient *http.Client
	// NoRetry returns a 429 as an *APIError instead of one wait and retry.
	NoRetry bool
}

// New creates a new Discord REST client.
func New(token string) *Client {
	return &Client{
		Token:      token,
		BaseURL:    defaultBaseURL,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// APIError is an HTTP error answer of the Discord API.
type APIError struct {
	Status     int
	Body       string
	RetryAfter time.Duration // set on 429
}

func (e *APIError) Error() string {
	return fmt.Sprintf("discord API error %d: %s", e.Status, e.Body)
}

// Do executes an HTTP request and returns the response. It retries a 429
// once, with the same body, unless NoRetry is set.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	var data []byte
	if body != nil {
		var err error
		if data, err = io.ReadAll(body); err != nil {
			return nil, err
		}
	}
	resp, err := c.send(ctx, method, path, data, "application/json")
	if err != nil || resp.StatusCode != http.StatusTooManyRequests || c.NoRetry {
		return resp, err
	}
	wait := retryAfter(resp)
	resp.Body.Close()
	if wait > 30*time.Second {
		wait = 30 * time.Second
	}
	select {
	case <-time.After(wait):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return c.send(ctx, method, path, data, "application/json")
}

func (c *Client) send(ctx context.Context, method, path string, data []byte, contentType string) (*http.Response, error) {
	var body io.Reader
	if data != nil {
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bot "+c.Token)
	req.Header.Set("User-Agent", userAgent)
	if data != nil {
		req.Header.Set("Content-Type", contentType)
	}
	return c.HTTPClient.Do(req)
}

// retryAfter reads the Retry-After header in seconds. The default is 1 s.
func retryAfter(resp *http.Response) time.Duration {
	secs, _ := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64)
	if secs <= 0 {
		secs = 1
	}
	return time.Duration(secs * float64(time.Second))
}

// check returns an *APIError for a status of 400 or more.
func check(resp *http.Response) error {
	if resp.StatusCode < 400 {
		return nil
	}
	b, _ := io.ReadAll(resp.Body)
	e := &APIError{Status: resp.StatusCode, Body: string(b)}
	if resp.StatusCode == http.StatusTooManyRequests {
		e.RetryAfter = retryAfter(resp)
	}
	return e
}

// DoJSON executes a request with a JSON body and decodes the JSON response.
func (c *Client) DoJSON(ctx context.Context, method, path string, reqBody, result any) error {
	var body io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	resp, err := c.Do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := check(resp); err != nil {
		return err
	}

	if result != nil && resp.StatusCode != 204 {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}

// DoNoBody executes a request with no body and no response parsing.
func (c *Client) DoNoBody(ctx context.Context, method, path string) error {
	resp, err := c.Do(ctx, method, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := check(resp); err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// FileUpload describes a single file in a multipart upload. Data, when
// set, replaces the contents of Path.
type FileUpload struct {
	Filename string
	Path     string
	Data     []byte
}

// Upload sends a multipart form request (for file uploads).
func (c *Client) Upload(ctx context.Context, path string, fields map[string]string, files []FileUpload) (*Message, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, err
		}
	}

	for i, f := range files {
		part, err := w.CreateFormFile(fmt.Sprintf("files[%d]", i), f.Filename)
		if err != nil {
			return nil, err
		}
		data := f.Data
		if data == nil {
			if data, err = os.ReadFile(f.Path); err != nil {
				return nil, fmt.Errorf("open file %s: %w", f.Path, err)
			}
		}
		if _, err := part.Write(data); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	resp, err := c.send(ctx, "POST", path, buf.Bytes(), w.FormDataContentType())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := check(resp); err != nil {
		return nil, err
	}

	var msg Message
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		return nil, err
	}
	return &msg, nil
}
