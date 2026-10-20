// Package client calls the Slack Web API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/flarco/cli-tools/slack-cli/internal/config"
)

// DefaultBaseURL is the Web API root for OAuth tokens.
const DefaultBaseURL = "https://slack.com/api/"

// browserUA is the User-Agent for xoxc- session tokens. Slack expects
// browser traffic for these tokens.
const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"

// Client sends Web API calls for one workspace.
type Client struct {
	token   string
	cookie  string
	baseURL string
	http    *http.Client
	// UserAgent is sent with OAuth tokens.
	UserAgent string
	// Verbose prints requests to stderr.
	Verbose bool
	// MaxRetries is the number of retries after HTTP 429.
	MaxRetries int
	// Refresh gets new credentials after an auth error. It is called once.
	Refresh func(ctx context.Context) (config.Workspace, error)
}

// New returns a client for the workspace credentials. SLACK_API_URL
// overrides the API root.
func New(w config.Workspace) *Client {
	c := &Client{MaxRetries: 4, UserAgent: "slack-cli"}
	c.setCredentials(w)
	return c
}

func (c *Client) setCredentials(w config.Workspace) {
	c.token, c.cookie = w.Token, w.Cookie
	c.baseURL = BaseURL(w)
	jar, _ := cookiejar.New(nil)
	if c.cookie != "" {
		for _, host := range []string{"https://slack.com", "https://" + hostOf(c.baseURL)} {
			u, _ := url.Parse(host)
			jar.SetCookies(u, []*http.Cookie{{Name: "d", Value: c.cookie, Path: "/", Domain: ".slack.com", Secure: true}})
		}
	}
	c.http = &http.Client{Jar: jar, Timeout: 5 * time.Minute}
}

// BaseURL returns the API root for the workspace. Session tokens use the
// team URL (Enterprise Grid needs it). It always ends in "/".
func BaseURL(w config.Workspace) string {
	if v := os.Getenv("SLACK_API_URL"); v != "" {
		return strings.TrimRight(v, "/") + "/"
	}
	if w.Kind() == "session" && strings.HasPrefix(w.URL, "https://") && strings.HasSuffix(hostOf(w.URL), ".slack.com") {
		return strings.TrimRight(w.URL, "/") + "/api/"
	}
	return DefaultBaseURL
}

func hostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.Host
}

// SetBaseURL replaces the API root, for example with a fake Slack.
func (c *Client) SetBaseURL(u string) { c.baseURL = strings.TrimRight(u, "/") + "/" }

// Session reports whether the client uses an xoxc- session token.
func (c *Client) Session() bool { return config.TokenKind(c.token) == "session" }

// HTTP returns the HTTP client (it has the "d" cookie).
func (c *Client) HTTP() *http.Client { return c.http }

// Token returns the token.
func (c *Client) Token() string { return c.token }

// Cookie returns the "d" cookie.
func (c *Client) Cookie() string { return c.cookie }

// APIError is a response with "ok": false.
type APIError struct {
	Method   string
	Code     string
	Needed   string
	Provided string
	Detail   string
	Body     json.RawMessage
}

func (e *APIError) Error() string {
	s := e.Method + ": " + e.Code
	if e.Detail != "" {
		s += " (" + e.Detail + ")"
	}
	if e.Needed != "" {
		s += fmt.Sprintf(" [needed scope: %s; token has: %s]", e.Needed, e.Provided)
	}
	return s
}

// IsAuth reports whether the error says that the token is not valid.
func (e *APIError) IsAuth() bool {
	switch e.Code {
	case "invalid_auth", "not_authed", "token_expired", "token_revoked", "account_inactive":
		return true
	}
	return false
}

// RateLimitError is HTTP 429 after all retries.
type RateLimitError struct {
	Method     string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s: rate limited (retry after %s)", e.Method, e.RetryAfter)
}

// Call sends a Web API method with form params and returns the response
// body. A response with "ok": false is an *APIError.
func (c *Client) Call(ctx context.Context, method string, params url.Values) (json.RawMessage, error) {
	body, err := c.call(ctx, method, params)
	var ae *APIError
	if errors.As(err, &ae) && ae.IsAuth() && c.Refresh != nil {
		refresh := c.Refresh
		c.Refresh = nil
		w, rerr := refresh(ctx)
		if rerr != nil {
			return nil, fmt.Errorf("%w (refresh failed: %v)", err, rerr)
		}
		c.setCredentials(w)
		return c.call(ctx, method, params)
	}
	return body, err
}

// CallJSON sends a method and decodes the response into v.
func (c *Client) CallJSON(ctx context.Context, method string, params url.Values, v any) error {
	body, err := c.Call(ctx, method, params)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

func (c *Client) call(ctx context.Context, method string, params url.Values) (json.RawMessage, error) {
	form := url.Values{}
	for k, v := range params {
		form[k] = v
	}
	for attempt := 0; ; attempt++ {
		req, err := c.newRequest(ctx, method, form)
		if err != nil {
			return nil, err
		}
		body, status, retry, err := c.send(req)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", method, err)
		}
		if status == http.StatusTooManyRequests {
			if attempt >= c.MaxRetries {
				return nil, &RateLimitError{Method: method, RetryAfter: retry}
			}
			if c.Verbose {
				fmt.Fprintf(os.Stderr, "< 429, wait %s\n", retry)
			}
			select {
			case <-time.After(retry):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return checkBody(method, status, body)
	}
}

func (c *Client) newRequest(ctx context.Context, method string, form url.Values) (*http.Request, error) {
	if c.Session() {
		// Browser clients send the session token in the form body.
		form.Set("token", c.token)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+method, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.authorize(req)
	if c.Verbose {
		shown := url.Values{}
		for k, v := range form {
			if k != "token" {
				shown[k] = v
			}
		}
		fmt.Fprintf(os.Stderr, "> POST %s %s\n", req.URL, shown.Encode())
	}
	return req, nil
}

// authorize sets the auth and client headers of a request to Slack.
func (c *Client) authorize(req *http.Request) {
	if c.Session() {
		for k, v := range BrowserHeaders() {
			req.Header.Set(k, v)
		}
		return
	}
	req.Header.Set("User-Agent", c.UserAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// BrowserHeaders are the headers a browser sends on a Slack API call.
func BrowserHeaders() map[string]string {
	return map[string]string{
		"User-Agent":         browserUA,
		"Accept":             "*/*",
		"Accept-Language":    "en-US,en;q=0.9",
		"Origin":             "https://app.slack.com",
		"Sec-Fetch-Site":     "same-site",
		"Sec-Fetch-Mode":     "cors",
		"Sec-Fetch-Dest":     "empty",
		"Sec-Ch-Ua":          `"Not;A=Brand";v="8", "Chromium";v="150", "Google Chrome";v="150"`,
		"Sec-Ch-Ua-Mobile":   "?0",
		"Sec-Ch-Ua-Platform": `"macOS"`,
	}
}

// WebSocketHeaders are the headers a browser sends on a WebSocket upgrade.
func WebSocketHeaders() http.Header {
	h := http.Header{}
	h.Set("User-Agent", browserUA)
	h.Set("Accept-Language", "en-US,en;q=0.9")
	h.Set("Cache-Control", "no-cache")
	h.Set("Pragma", "no-cache")
	h.Set("Origin", "https://app.slack.com")
	return h
}

func (c *Client) send(req *http.Request) ([]byte, int, time.Duration, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if c.Verbose {
		fmt.Fprintf(os.Stderr, "< %d (%d bytes)\n", resp.StatusCode, len(body))
	}
	return body, resp.StatusCode, retryAfter(resp.Header.Get("Retry-After")), err
}

func retryAfter(h string) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && s >= 0 {
		return min(time.Duration(s)*time.Second, 2*time.Minute) + 250*time.Millisecond
	}
	return 5 * time.Second
}

func checkBody(method string, status int, body []byte) (json.RawMessage, error) {
	var env struct {
		OK       *bool  `json:"ok"`
		Error    string `json:"error"`
		Needed   string `json:"needed"`
		Provided string `json:"provided"`
		Detail   string `json:"detail"`
		Meta     struct {
			Messages []string `json:"messages"`
		} `json:"response_metadata"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.OK == nil {
		if status < 200 || status > 299 {
			return nil, fmt.Errorf("%s: HTTP %d: %s", method, status, snippet(body))
		}
		return nil, fmt.Errorf("%s: response is not a Slack envelope: %s", method, snippet(body))
	}
	if !*env.OK {
		detail := env.Detail
		if detail == "" && len(env.Meta.Messages) > 0 {
			detail = strings.Join(env.Meta.Messages, "; ")
		}
		return nil, &APIError{Method: method, Code: env.Error, Needed: env.Needed, Provided: env.Provided, Detail: detail, Body: body}
	}
	return body, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// Get downloads a URL with the workspace auth (for url_private files).
func (c *Client) Get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.authorize(req)
	if c.Session() {
		// File hosts take the session token as a query param.
		q := req.URL.Query()
		if q.Get("t") == "" {
			q.Set("t", c.token)
			req.URL.RawQuery = q.Encode()
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP %d", req.URL.Redacted(), resp.StatusCode)
	}
	return resp, nil
}

// PostMultipart sends a method as multipart/form-data. files maps a field
// name to a local file path.
func (c *Client) PostMultipart(ctx context.Context, method string, params url.Values, files map[string]string) (json.RawMessage, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if c.Session() {
		params.Set("token", c.token)
	}
	for k, vs := range params {
		for _, v := range vs {
			mw.WriteField(k, v)
		}
	}
	for field, path := range files {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		part, err := mw.CreateFormFile(field, baseName(path))
		if err == nil {
			_, err = io.Copy(part, f)
		}
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+method, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	c.authorize(req)
	body, status, _, err := c.send(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	return checkBody(method, status, body)
}

// Upload sends raw bytes to an upload URL from files.getUploadURLExternal.
func (c *Client) Upload(ctx context.Context, uploadURL string, r io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, r)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload: HTTP %d: %s", resp.StatusCode, snippet(b))
	}
	return nil
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
