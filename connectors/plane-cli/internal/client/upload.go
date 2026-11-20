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
	"path/filepath"
	"strings"
)

// UploadTarget is the presigned storage form that Plane returns when you
// create an attachment or asset.
type UploadTarget struct {
	URL    string            `json:"url"`
	Fields map[string]string `json:"fields"`
}

// FindUploadTarget looks for an "upload_data" object at the top level or one
// level down in a create response.
func FindUploadTarget(body []byte) (*UploadTarget, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return nil, false
	}
	if raw, ok := m["upload_data"]; ok {
		var t UploadTarget
		if json.Unmarshal(raw, &t) == nil && t.URL != "" {
			return &t, true
		}
	}
	for _, raw := range m {
		if t, ok := FindUploadTarget(raw); ok {
			return t, true
		}
	}
	return nil, false
}

// Upload posts the file to presigned storage as a multipart form. The form
// fields come first, then the file, as S3 requires.
func (c *Client) Upload(ctx context.Context, t *UploadTarget, filename string, content io.Reader) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range t.Fields {
		if err := w.WriteField(k, v); err != nil {
			return err
		}
	}
	part, err := w.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, content); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed (status %d): %s", resp.StatusCode, b)
	}
	return nil
}

// Download writes the file of a v1 attachment or asset endpoint to w. An
// attachment answers with a redirect to presigned storage, an asset with JSON
// that holds the presigned "asset_url". The client fetches storage without
// the API key.
func (c *Client) Download(ctx context.Context, path string, w io.Writer) (int64, error) {
	api := *c.httpClient
	api.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	if c.Verbose {
		fmt.Fprintf(os.Stderr, "→ GET %s\n", req.URL)
	}
	resp, err := api.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	var storageURL string
	switch {
	case resp.StatusCode >= 400:
		b, _ := io.ReadAll(resp.Body)
		return 0, newAPIError(resp.StatusCode, b)
	case resp.StatusCode >= 300:
		loc, err := resp.Location()
		if err != nil {
			return 0, fmt.Errorf("redirect without location: %w", err)
		}
		storageURL = loc.String()
	default:
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, err
		}
		var m struct {
			AssetURL string `json:"asset_url"`
		}
		if json.Unmarshal(b, &m) != nil || !strings.HasPrefix(m.AssetURL, "http") {
			return 0, fmt.Errorf("response has no download URL: %s", b)
		}
		storageURL = m.AssetURL
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, storageURL, nil)
	if err != nil {
		return 0, err
	}
	if c.Verbose {
		fmt.Fprintf(os.Stderr, "→ GET %s://%s%s\n", req.URL.Scheme, req.URL.Host, req.URL.Path)
	}
	// No timeout: large files take long; ctx stops the transfer.
	storage := &http.Client{Transport: c.httpClient.Transport}
	resp, err = storage.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("download failed (status %d): %s", resp.StatusCode, b)
	}
	return io.Copy(w, resp.Body)
}
