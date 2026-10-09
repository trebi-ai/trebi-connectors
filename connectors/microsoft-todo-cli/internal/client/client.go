// Package client is a small Microsoft Graph client for To Do, with the
// device code login and the token refresh.
package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Default endpoints.
const (
	GraphURL  = "https://graph.microsoft.com/v1.0"
	LoginBase = "https://login.microsoftonline.com"
)

// Scopes is what the login asks for. openid and profile give the id_token
// with the account name, so the login needs no Graph scope other than
// Tasks.ReadWrite.
const Scopes = "openid profile offline_access Tasks.ReadWrite"

// refreshScopes leave out openid and profile, so a login of v0.1.0, which
// did not ask for them, still refreshes.
const refreshScopes = "offline_access Tasks.ReadWrite"

// ErrAuth is a login that Microsoft does not accept and cannot refresh.
var ErrAuth = errors.New("the Microsoft login is not valid; log in again")

// ErrNoLogin is a client with no token.
var ErrNoLogin = errors.New("not logged in to Microsoft")

// APIError is a Graph or login error answer.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("microsoft %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("microsoft %d %s", e.Status, e.Code)
}

// Session is the content of auth.json: the tokens, the app that issued
// them, and the account. A refresh token works only with its app.
type Session struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	ClientID     string    `json:"client_id,omitempty"`
	Tenant       string    `json:"tenant,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	AccountName  string    `json:"account_name,omitempty"`
}

// LoadSession reads auth.json. A missing file is an empty session.
func LoadSession(path string) (Session, error) {
	var s Session
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("read login: %w", err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("parse login %s: %w", path, err)
	}
	return s, nil
}

// Save writes the session with a temp file and a rename.
func (s Session) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(path, data)
}

// WriteFile writes data with a temp file and a rename, mode 0600.
func WriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after the rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck // the write error wins
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Client calls Graph as one user.
type Client struct {
	GraphURL   string
	LoginBase  string
	ClientID   string
	Tenant     string
	HTTPClient *http.Client
	// OnRefresh saves a refreshed session. Nil keeps it in memory.
	OnRefresh func(Session) error

	mu   sync.Mutex
	sess Session
}

// New builds a client for the app and the session.
func New(clientID, tenant string, s Session) *Client {
	return &Client{
		GraphURL: GraphURL, LoginBase: LoginBase, ClientID: clientID, Tenant: tenant,
		HTTPClient: &http.Client{Timeout: 60 * time.Second},
		sess:       s,
	}
}

// Session returns the current session.
func (c *Client) Session() Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess
}

// SetSession replaces the session.
func (c *Client) SetSession(s Session) {
	c.mu.Lock()
	c.sess = s
	c.mu.Unlock()
}

// loginURL is the OAuth base of the app of the session, or of the client.
func (c *Client) loginURL(tenant string) string {
	if tenant == "" {
		tenant = c.Tenant
	}
	if tenant == "" {
		tenant = "common"
	}
	return c.LoginBase + "/" + url.PathEscape(tenant) + "/oauth2/v2.0"
}

// Request is one raw Graph request.
type Request struct {
	Method      string
	Path        string // relative to GraphURL, or a full Graph URL
	Body        []byte
	ContentType string
	Headers     map[string]string
}

// Response is one raw Graph answer.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Do sends one Graph request with a JSON body and decodes a JSON answer.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	r := Request{Method: method, Path: path}
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r.Body, r.ContentType = data, "application/json"
	}
	resp, err := c.Send(ctx, r)
	if err != nil {
		return err
	}
	if out == nil || len(resp.Body) == 0 {
		return nil
	}
	return json.Unmarshal(resp.Body, out)
}

// Send sends one raw Graph request. A 401 refreshes the token once. An
// answer of 300 or more is an *APIError.
func (c *Client) Send(ctx context.Context, r Request) (Response, error) {
	u := r.Path
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = c.GraphURL + u
	} else if !strings.HasPrefix(u, c.GraphURL+"/") {
		return Response{}, fmt.Errorf("refuse to send the token to %s", u)
	}
	for attempt := 0; ; attempt++ {
		tok, err := c.token(ctx, attempt > 0)
		if err != nil {
			return Response{}, err
		}
		req, err := http.NewRequestWithContext(ctx, r.Method, u, bytes.NewReader(r.Body))
		if err != nil {
			return Response{}, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if r.ContentType != "" {
			req.Header.Set("Content-Type", r.ContentType)
		}
		for k, v := range r.Headers {
			req.Header.Set(k, v)
		}
		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			return Response{}, err
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close() //nolint:errcheck // the body is read
		if err != nil {
			return Response{}, err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return Response{}, ErrAuth
		}
		if resp.StatusCode >= 300 {
			return Response{}, graphError(resp.StatusCode, raw)
		}
		return Response{Status: resp.StatusCode, Header: resp.Header, Body: raw}, nil
	}
}

// token returns an access token. It refreshes the token when it is close
// to the expiry, or when force is set.
func (c *Client) token(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess.AccessToken == "" {
		return "", ErrNoLogin
	}
	if !force && time.Until(c.sess.ExpiresAt) > time.Minute {
		return c.sess.AccessToken, nil
	}
	if c.sess.RefreshToken == "" {
		return "", ErrAuth
	}
	clientID := c.sess.ClientID
	if clientID == "" {
		clientID = c.ClientID
	}
	t, err := c.tokenRequest(ctx, c.sess.Tenant, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {c.sess.RefreshToken},
		"scope":         {refreshScopes},
	})
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status < 500 {
		return "", ErrAuth
	}
	if err != nil {
		return "", err
	}
	c.sess.AccessToken, c.sess.ExpiresAt = t.AccessToken, t.expiry()
	if t.RefreshToken != "" {
		c.sess.RefreshToken = t.RefreshToken
	}
	if c.OnRefresh != nil {
		if err := c.OnRefresh(c.sess); err != nil {
			return "", fmt.Errorf("save login: %w", err)
		}
	}
	return c.sess.AccessToken, nil
}

// DeviceCode is the answer of /devicecode.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	Message         string `json:"message"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func (t tokenResponse) expiry() time.Time {
	return time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
}

// account reads the account from the id_token. The token comes straight
// from the login endpoint over TLS, so the signature is not checked.
func (t tokenResponse) account() (id, name string) {
	parts := strings.Split(t.IDToken, ".")
	if len(parts) != 3 {
		return "", ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var c struct {
		OID               string `json:"oid"`
		Sub               string `json:"sub"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return "", ""
	}
	id, name = c.OID, c.PreferredUsername
	if id == "" {
		id = c.Sub
	}
	if name == "" {
		name = c.Name
	}
	return id, name
}

// BeginDeviceCode starts a device code login.
func (c *Client) BeginDeviceCode(ctx context.Context) (DeviceCode, error) {
	if c.ClientID == "" {
		return DeviceCode{}, errors.New("no Microsoft app id: set MICROSOFT_TODO_CLIENT_ID")
	}
	var dc DeviceCode
	err := c.form(ctx, c.loginURL("")+"/devicecode", url.Values{"client_id": {c.ClientID}, "scope": {Scopes}}, &dc)
	return dc, err
}

// WaitDeviceCode polls /token until the person finishes the login, the
// code expires, or ctx ends. It returns the new session with the app and
// the account.
func (c *Client) WaitDeviceCode(ctx context.Context, dc DeviceCode) (Session, error) {
	every := time.Duration(dc.Interval) * time.Second
	if every <= 0 {
		every = 200 * time.Millisecond
	}
	for {
		select {
		case <-ctx.Done():
			return Session{}, ctx.Err()
		case <-time.After(every):
		}
		t, err := c.tokenRequest(ctx, "", url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"client_id":   {c.ClientID},
			"device_code": {dc.DeviceCode},
		})
		var apiErr *APIError
		switch {
		case err == nil:
			s := Session{
				AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, ExpiresAt: t.expiry(),
				ClientID: c.ClientID, Tenant: c.Tenant,
			}
			s.AccountID, s.AccountName = t.account()
			return s, nil
		case errors.As(err, &apiErr) && apiErr.Code == "authorization_pending":
		case errors.As(err, &apiErr) && apiErr.Code == "slow_down":
			every += 5 * time.Second
		case errors.As(err, &apiErr) && apiErr.Code == "expired_token":
			return Session{}, errors.New("the code expired before the login finished")
		case errors.As(err, &apiErr) && apiErr.Code == "authorization_declined":
			return Session{}, errors.New("the login was declined")
		default:
			return Session{}, err
		}
	}
}

func (c *Client) tokenRequest(ctx context.Context, tenant string, v url.Values) (tokenResponse, error) {
	var t tokenResponse
	err := c.form(ctx, c.loginURL(tenant)+"/token", v, &t)
	if err == nil && t.AccessToken == "" {
		err = errors.New("the token answer has no access token")
	}
	return t, err
}

// form posts a form to a login endpoint.
func (c *Client) form(ctx context.Context, u string, v url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(v.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(raw, &e) //nolint:errcheck // a body that does not decode keeps the status
		return &APIError{Status: resp.StatusCode, Code: e.Error, Message: e.Description}
	}
	return json.Unmarshal(raw, out)
}

func graphError(status int, raw []byte) error {
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e) //nolint:errcheck // a body that does not decode keeps the status
	return &APIError{Status: status, Code: e.Error.Code, Message: e.Error.Message}
}

// IsNotFound reports a 404 answer.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// Secret returns a random URL-safe string of n bytes.
func Secret(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) //nolint:errcheck // crypto/rand.Read never fails
	return base64.RawURLEncoding.EncodeToString(b)
}
