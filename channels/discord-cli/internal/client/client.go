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
	userAgent      = "DiscordBot (https://github.com/flarco/cli-tools, 1.0)"
)

// Client is a Discord REST API client.
type Client struct {
	Token      string
	BaseURL    string
	HTTPClient *http.Client
}

// New creates a new Discord REST client.
func New(token string) *Client {
	return &Client{
		Token:      token,
		BaseURL:    defaultBaseURL,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Do executes an HTTP request and returns the response body.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	url := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bot "+c.Token)
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == 429 {
		retryAfter := resp.Header.Get("Retry-After")
		resp.Body.Close()
		secs, _ := strconv.ParseFloat(retryAfter, 64)
		if secs <= 0 {
			secs = 1
		}
		if secs > 30 {
			secs = 30
		}
		time.Sleep(time.Duration(secs*1000) * time.Millisecond)

		req2, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			return nil, err
		}
		req2.Header.Set("Authorization", "Bot "+c.Token)
		req2.Header.Set("User-Agent", userAgent)
		resp, err = c.HTTPClient.Do(req2)
		if err != nil {
			return nil, err
		}
	}

	return resp, nil
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

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("discord API error %d: %s", resp.StatusCode, string(b))
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

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("discord API error %d: %s", resp.StatusCode, string(b))
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// FileUpload describes a single file in a multipart upload.
type FileUpload struct {
	Filename string
	Path     string
}

// Upload sends a multipart form request (for file uploads).
func (c *Client) Upload(ctx context.Context, path string, fields map[string]string, files []FileUpload) (*Message, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	for k, v := range fields {
		w.WriteField(k, v)
	}

	for i, f := range files {
		fieldName := fmt.Sprintf("files[%d]", i)
		part, err := w.CreateFormFile(fieldName, f.Filename)
		if err != nil {
			return nil, err
		}
		fh, err := os.Open(f.Path)
		if err != nil {
			return nil, fmt.Errorf("open file %s: %w", f.Path, err)
		}
		_, err = io.Copy(part, fh)
		fh.Close()
		if err != nil {
			return nil, err
		}
	}
	w.Close()

	url := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, "POST", url, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bot "+c.Token)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("discord API error %d: %s", resp.StatusCode, string(b))
	}

	var msg Message
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		return nil, err
	}
	return &msg, nil
}
