// Package client is a small GitHub REST client: repositories, events,
// repository webhooks, and the OAuth device flow.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Default base URLs.
const (
	DefaultAPI = "https://api.github.com"
	DefaultWeb = "https://github.com"
)

// Client calls the GitHub API with one token.
type Client struct {
	API   string // REST base URL
	Web   string // base URL of the device flow
	Token string
	HTTP  *http.Client
}

// New returns a client for api.github.com.
func New(token string) *Client {
	return &Client{API: DefaultAPI, Web: DefaultWeb, Token: token, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// APIError is a GitHub answer that is not 2xx.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("github: %d %s", e.Status, e.Message) }

// StatusOf returns the HTTP status of an APIError, or 0.
func StatusOf(err error) int {
	var e *APIError
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// User is a GitHub account.
type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name,omitempty"`
	Type  string `json:"type,omitempty"`
}

// Repo is one repository.
type Repo struct {
	ID          int64  `json:"id"`
	FullName    string `json:"full_name"`
	Description string `json:"description,omitempty"`
	Private     bool   `json:"private"`
	Permissions struct {
		Admin bool `json:"admin"`
	} `json:"permissions"`
}

// HookConfig is the config of a repository webhook.
type HookConfig struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type,omitempty"`
	Secret      string `json:"secret,omitempty"`
	InsecureSSL string `json:"insecure_ssl,omitempty"`
}

// Hook is one repository webhook.
type Hook struct {
	ID     int64      `json:"id,omitempty"`
	Name   string     `json:"name,omitempty"`
	Active bool       `json:"active"`
	Events []string   `json:"events"`
	Config HookConfig `json:"config"`
}

// Event is one item of the repository events API.
type Event struct {
	ID        string                `json:"id"`
	Type      string                `json:"type"`
	Actor     User                  `json:"actor"`
	Repo      struct{ Name string } `json:"repo"`
	Payload   json.RawMessage       `json:"payload"`
	CreatedAt string                `json:"created_at"`
}

// EventPage is one answer of the events API. NotModified is a 304 for the
// ETag. Interval is X-Poll-Interval.
type EventPage struct {
	Events      []Event
	ETag        string
	NotModified bool
	Interval    time.Duration
}

func (c *Client) do(ctx context.Context, method, base, path string, body, out any, hdr http.Header) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "github-cli-trebi")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" && base == c.API {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // read only
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotModified {
		return resp, nil
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e) //nolint:errcheck // a body that is not JSON keeps the status text
		return resp, &APIError{Status: resp.StatusCode, Message: cmpOr(e.Message, resp.Status)}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp, fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	return resp, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Me returns the account of the token.
func (c *Client) Me(ctx context.Context) (User, error) {
	var u User
	_, err := c.do(ctx, http.MethodGet, c.API, "/user", nil, &u, nil)
	return u, err
}

// Repos lists one page of the repositories of the account, newest push
// first. Page starts at 1. more is true when a next page exists.
func (c *Client) Repos(ctx context.Context, page int) (repos []Repo, more bool, err error) {
	q := url.Values{"per_page": {"100"}, "sort": {"pushed"}, "page": {strconv.Itoa(max(page, 1))}}
	resp, err := c.do(ctx, http.MethodGet, c.API, "/user/repos?"+q.Encode(), nil, &repos, nil)
	if err != nil {
		return nil, false, err
	}
	return repos, strings.Contains(resp.Header.Get("Link"), `rel="next"`), nil
}

// Hooks lists the webhooks of a repository.
func (c *Client) Hooks(ctx context.Context, repo string) ([]Hook, error) {
	var hooks []Hook
	_, err := c.do(ctx, http.MethodGet, c.API, "/repos/"+repo+"/hooks?per_page=100", nil, &hooks, nil)
	return hooks, err
}

// CreateHook adds a webhook to a repository.
func (c *Client) CreateHook(ctx context.Context, repo string, h Hook) (Hook, error) {
	h.Name = "web"
	var out Hook
	_, err := c.do(ctx, http.MethodPost, c.API, "/repos/"+repo+"/hooks", h, &out, nil)
	return out, err
}

// UpdateHook replaces the events and the config of a webhook.
func (c *Client) UpdateHook(ctx context.Context, repo string, id int64, h Hook) (Hook, error) {
	var out Hook
	_, err := c.do(ctx, http.MethodPatch, c.API, "/repos/"+repo+"/hooks/"+strconv.FormatInt(id, 10), h, &out, nil)
	return out, err
}

// DeleteHook removes a webhook. A hook that is gone is not an error.
func (c *Client) DeleteHook(ctx context.Context, repo string, id int64) error {
	_, err := c.do(ctx, http.MethodDelete, c.API, "/repos/"+repo+"/hooks/"+strconv.FormatInt(id, 10), nil, nil, nil)
	if StatusOf(err) == http.StatusNotFound {
		return nil
	}
	return err
}

// Events reads the newest events of a repository. etag makes the read
// conditional.
func (c *Client) Events(ctx context.Context, repo, etag string) (EventPage, error) {
	hdr := http.Header{}
	if etag != "" {
		hdr.Set("If-None-Match", etag)
	}
	var page EventPage
	resp, err := c.do(ctx, http.MethodGet, c.API, "/repos/"+repo+"/events?per_page=100", nil, &page.Events, hdr)
	if err != nil {
		return EventPage{}, err
	}
	page.NotModified = resp.StatusCode == http.StatusNotModified
	page.ETag = cmpOr(resp.Header.Get("ETag"), etag)
	if n, err := strconv.Atoi(resp.Header.Get("X-Poll-Interval")); err == nil {
		page.Interval = time.Duration(n) * time.Second
	}
	return page, nil
}

// DeviceCode is the start of a device flow.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// ErrPending is a device flow that waits for the user.
var ErrPending = errors.New("authorization pending")

// StartDevice starts a device flow for an OAuth app.
func (c *Client) StartDevice(ctx context.Context, clientID, scope string) (DeviceCode, error) {
	var d DeviceCode
	err := c.form(ctx, "/login/device/code", url.Values{"client_id": {clientID}, "scope": {scope}}, &d)
	return d, err
}

// PollDevice asks for the token of a device flow once. It returns
// ErrPending while the user has not approved.
func (c *Client) PollDevice(ctx context.Context, clientID, deviceCode string) (string, error) {
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	v := url.Values{"client_id": {clientID}, "device_code": {deviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}
	if err := c.form(ctx, "/login/oauth/access_token", v, &out); err != nil {
		return "", err
	}
	switch out.Error {
	case "":
		if out.AccessToken == "" {
			return "", errors.New("github gave no access token")
		}
		return out.AccessToken, nil
	case "authorization_pending", "slow_down":
		return "", ErrPending
	default:
		return "", errors.New(cmpOr(out.Description, out.Error))
	}
}

func (c *Client) form(ctx context.Context, path string, v url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Web+path, strings.NewReader(v.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // read only
	if resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Message: resp.Status}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
