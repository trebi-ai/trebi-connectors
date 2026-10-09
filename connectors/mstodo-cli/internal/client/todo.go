package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// User is the account of /me.
type User struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	UserPrincipalName string `json:"userPrincipalName"`
}

// Name is the name to show for the account.
func (u User) Name() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.UserPrincipalName
}

// List is a To Do list.
type List struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// Task is a To Do task. Removed is set on a delta item for a deleted task.
type Task struct {
	ID                   string          `json:"id"`
	Title                string          `json:"title"`
	Status               string          `json:"status,omitempty"`
	Importance           string          `json:"importance,omitempty"`
	Body                 *ItemBody       `json:"body,omitempty"`
	DueDateTime          *DateTimeZone   `json:"dueDateTime,omitempty"`
	CompletedDateTime    *DateTimeZone   `json:"completedDateTime,omitempty"`
	CreatedDateTime      string          `json:"createdDateTime,omitempty"`
	LastModifiedDateTime string          `json:"lastModifiedDateTime,omitempty"`
	Removed              json.RawMessage `json:"@removed,omitempty"`
}

// ItemBody is the note of a task.
type ItemBody struct {
	Content     string `json:"content"`
	ContentType string `json:"contentType"`
}

// DateTimeZone is a Graph date with a time zone.
type DateTimeZone struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

// Subscription is a Graph change notification subscription.
type Subscription struct {
	ID                       string    `json:"id,omitempty"`
	Resource                 string    `json:"resource"`
	ChangeType               string    `json:"changeType"`
	NotificationURL          string    `json:"notificationUrl"`
	LifecycleNotificationURL string    `json:"lifecycleNotificationUrl,omitempty"`
	ClientState              string    `json:"clientState,omitempty"`
	ExpirationDateTime       time.Time `json:"expirationDateTime"`
}

// page is one page of a Graph collection.
type page[T any] struct {
	Value     []T    `json:"value"`
	NextLink  string `json:"@odata.nextLink"`
	DeltaLink string `json:"@odata.deltaLink"`
}

// all follows the nextLinks of a collection. It returns the deltaLink of a
// delta query.
func all[T any](ctx context.Context, c *Client, path string) ([]T, string, error) {
	var out []T
	for path != "" {
		var p page[T]
		if err := c.Do(ctx, http.MethodGet, path, nil, &p); err != nil {
			return nil, "", err
		}
		out = append(out, p.Value...)
		if p.NextLink == "" {
			return out, p.DeltaLink, nil
		}
		path = p.NextLink
	}
	return out, "", nil
}

// Me returns the account.
func (c *Client) Me(ctx context.Context) (User, error) {
	var u User
	err := c.Do(ctx, http.MethodGet, "/me", nil, &u)
	return u, err
}

// Lists returns the To Do lists.
func (c *Client) Lists(ctx context.Context) ([]List, error) {
	l, _, err := all[List](ctx, c, "/me/todo/lists")
	return l, err
}

// List returns one list.
func (c *Client) List(ctx context.Context, id string) (List, error) {
	var l List
	err := c.Do(ctx, http.MethodGet, "/me/todo/lists/"+url.PathEscape(id), nil, &l)
	return l, err
}

// Tasks returns the tasks of a list.
func (c *Client) Tasks(ctx context.Context, list string) ([]Task, error) {
	t, _, err := all[Task](ctx, c, "/me/todo/lists/"+url.PathEscape(list)+"/tasks")
	return t, err
}

// Task returns one task.
func (c *Client) Task(ctx context.Context, list, id string) (Task, error) {
	var t Task
	err := c.Do(ctx, http.MethodGet, "/me/todo/lists/"+url.PathEscape(list)+"/tasks/"+url.PathEscape(id), nil, &t)
	return t, err
}

// Delta runs a delta query from link, or from the start when link is
// empty. It returns the changes and the next deltaLink.
func (c *Client) Delta(ctx context.Context, list, link string) ([]Task, string, error) {
	if link == "" {
		link = "/me/todo/lists/" + url.PathEscape(list) + "/tasks/delta"
	}
	return all[Task](ctx, c, link)
}

// Subscriptions returns the subscriptions of this app.
func (c *Client) Subscriptions(ctx context.Context) ([]Subscription, error) {
	s, _, err := all[Subscription](ctx, c, "/subscriptions")
	return s, err
}

// CreateSubscription creates a subscription. Graph checks the
// notification URLs before it answers.
func (c *Client) CreateSubscription(ctx context.Context, s Subscription) (Subscription, error) {
	var out Subscription
	err := c.Do(ctx, http.MethodPost, "/subscriptions", s, &out)
	return out, err
}

// RenewSubscription moves the expiry of a subscription.
func (c *Client) RenewSubscription(ctx context.Context, id string, exp time.Time) (Subscription, error) {
	var out Subscription
	err := c.Do(ctx, http.MethodPatch, "/subscriptions/"+url.PathEscape(id), map[string]any{"expirationDateTime": exp.UTC()}, &out)
	return out, err
}

// DeleteSubscription deletes a subscription. A missing one is not an error.
func (c *Client) DeleteSubscription(ctx context.Context, id string) error {
	err := c.Do(ctx, http.MethodDelete, "/subscriptions/"+url.PathEscape(id), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}
