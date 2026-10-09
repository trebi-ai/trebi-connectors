// Package client is a small client of the Linear GraphQL API.
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

// DefaultURL is the Linear GraphQL endpoint.
const DefaultURL = "https://api.linear.app/graphql"

// Team is one Linear team.
type Team struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// User is a Linear user.
type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

// State is the workflow state of an issue.
type State struct {
	Name string `json:"name"`
}

// Issue is one Linear issue.
type Issue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	URL        string `json:"url,omitempty"`
	CreatedAt  string `json:"createdAt,omitempty"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
	Team       *Team  `json:"team,omitempty"`
	Creator    *User  `json:"creator,omitempty"`
	State      *State `json:"state,omitempty"`
}

// Comment is one comment on an issue.
type Comment struct {
	ID        string `json:"id"`
	Body      string `json:"body"`
	URL       string `json:"url,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
	User      *User  `json:"user,omitempty"`
	Issue     *Issue `json:"issue,omitempty"`
}

// Webhook is one Linear webhook.
type Webhook struct {
	ID             string   `json:"id"`
	URL            string   `json:"url"`
	Label          string   `json:"label,omitempty"`
	Enabled        bool     `json:"enabled"`
	AllPublicTeams bool     `json:"allPublicTeams"`
	ResourceTypes  []string `json:"resourceTypes"`
	Secret         string   `json:"secret,omitempty"`
	Team           *Team    `json:"team,omitempty"`
}

// WebhookInput creates a webhook. TeamID empty means every public team.
type WebhookInput struct {
	URL           string
	TeamID        string
	ResourceTypes []string
	Secret        string
	Label         string
}

// Error is an HTTP or GraphQL error.
type Error struct {
	Status  int
	Type    string
	Message string
}

func (e *Error) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("linear: %s (%s)", e.Message, e.Type)
	}
	return "linear: " + e.Message
}

// Unauthorized reports a bad or revoked key.
func (e *Error) Unauthorized() bool {
	return e.Status == http.StatusUnauthorized || strings.Contains(strings.ToLower(e.Type), "authentication")
}

// Forbidden reports a missing permission, for example a webhook on a team
// by a user who is not an admin.
func (e *Error) Forbidden() bool {
	t := strings.ToLower(e.Type + " " + e.Message)
	return e.Status == http.StatusForbidden || strings.Contains(t, "forbidden") || strings.Contains(t, "permission") || strings.Contains(t, "admin")
}

// Client calls the API with one key.
type Client struct {
	URL  string
	key  string
	http *http.Client
}

// New builds a client for a personal API key.
func New(key string) *Client {
	return &Client{URL: DefaultURL, key: key, http: &http.Client{Timeout: 30 * time.Second}}
}

// Key reports whether the client has a key.
func (c *Client) Key() bool { return c.key != "" }

// do runs one operation and decodes data into out.
func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // read only
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	var r struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message    string `json:"message"`
			Extensions struct {
				Type string `json:"type"`
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return &Error{Status: resp.StatusCode, Message: resp.Status}
	}
	if len(r.Errors) > 0 {
		e := r.Errors[0]
		return &Error{Status: resp.StatusCode, Type: e.Extensions.Type + e.Extensions.Code, Message: e.Message}
	}
	if resp.StatusCode >= 300 {
		return &Error{Status: resp.StatusCode, Message: resp.Status}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(r.Data, out)
}

// Viewer returns the user of the key.
func (c *Client) Viewer(ctx context.Context) (User, error) {
	var r struct {
		Viewer User `json:"viewer"`
	}
	err := c.do(ctx, `query Viewer { viewer { id name email } }`, nil, &r)
	return r.Viewer, err
}

// Teams lists the teams that the key can read.
func (c *Client) Teams(ctx context.Context) ([]Team, error) {
	var r struct {
		Teams struct {
			Nodes []Team `json:"nodes"`
		} `json:"teams"`
	}
	err := c.do(ctx, `query Teams { teams(first: 250) { nodes { id key name } } }`, nil, &r)
	return r.Teams.Nodes, err
}

// Issues lists the issues changed after a time, oldest first. An empty
// after lists the newest issues.
func (c *Client) Issues(ctx context.Context, after string, limit int) ([]Issue, error) {
	var r struct {
		Issues struct {
			Nodes []Issue `json:"nodes"`
		} `json:"issues"`
	}
	vars := map[string]any{"first": limit, "filter": updatedFilter(after)}
	err := c.do(ctx, `query Issues($first: Int, $filter: IssueFilter) { issues(first: $first, filter: $filter, orderBy: updatedAt) { nodes { id identifier title url createdAt updatedAt team { id key name } creator { id name } state { name } } } }`, vars, &r)
	return r.Issues.Nodes, err
}

// Comments lists the comments changed after a time.
func (c *Client) Comments(ctx context.Context, after string, limit int) ([]Comment, error) {
	var r struct {
		Comments struct {
			Nodes []Comment `json:"nodes"`
		} `json:"comments"`
	}
	vars := map[string]any{"first": limit, "filter": updatedFilter(after)}
	err := c.do(ctx, `query Comments($first: Int, $filter: CommentFilter) { comments(first: $first, filter: $filter, orderBy: updatedAt) { nodes { id body url createdAt updatedAt user { id name } issue { id identifier title team { id key name } } } } }`, vars, &r)
	return r.Comments.Nodes, err
}

func updatedFilter(after string) map[string]any {
	if after == "" {
		return nil
	}
	return map[string]any{"updatedAt": map[string]any{"gt": after}}
}

// Webhooks lists the webhooks of the workspace.
func (c *Client) Webhooks(ctx context.Context) ([]Webhook, error) {
	var r struct {
		Webhooks struct {
			Nodes []Webhook `json:"nodes"`
		} `json:"webhooks"`
	}
	err := c.do(ctx, `query Webhooks { webhooks(first: 100) { nodes { id url label enabled allPublicTeams resourceTypes team { id key name } } } }`, nil, &r)
	return r.Webhooks.Nodes, err
}

// CreateWebhook makes a webhook and returns it with its secret.
func (c *Client) CreateWebhook(ctx context.Context, in WebhookInput) (Webhook, error) {
	input := map[string]any{"url": in.URL, "resourceTypes": in.ResourceTypes, "label": in.Label}
	if in.Secret != "" {
		input["secret"] = in.Secret
	}
	if in.TeamID != "" {
		input["teamId"] = in.TeamID
	} else {
		input["allPublicTeams"] = true
	}
	var r struct {
		WebhookCreate struct {
			Success bool    `json:"success"`
			Webhook Webhook `json:"webhook"`
		} `json:"webhookCreate"`
	}
	err := c.do(ctx, `mutation WebhookCreate($input: WebhookCreateInput!) { webhookCreate(input: $input) { success webhook { id url enabled allPublicTeams resourceTypes secret team { id key name } } } }`, map[string]any{"input": input}, &r)
	if err == nil && !r.WebhookCreate.Success {
		err = &Error{Message: "webhookCreate did not succeed"}
	}
	return r.WebhookCreate.Webhook, err
}

// UpdateWebhook changes the resource types of a webhook.
func (c *Client) UpdateWebhook(ctx context.Context, id string, resourceTypes []string) error {
	return c.do(ctx, `mutation WebhookUpdate($id: String!, $input: WebhookUpdateInput!) { webhookUpdate(id: $id, input: $input) { success } }`,
		map[string]any{"id": id, "input": map[string]any{"resourceTypes": resourceTypes}}, nil)
}

// DeleteWebhook removes a webhook.
func (c *Client) DeleteWebhook(ctx context.Context, id string) error {
	return c.do(ctx, `mutation WebhookDelete($id: String!) { webhookDelete(id: $id) { success } }`, map[string]any{"id": id}, nil)
}
