package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// List is a To Do list. Removed is set on a delta item for a deleted list.
type List struct {
	ID                string          `json:"id"`
	DisplayName       string          `json:"displayName"`
	IsOwner           bool            `json:"isOwner,omitempty"`
	IsShared          bool            `json:"isShared,omitempty"`
	WellknownListName string          `json:"wellknownListName,omitempty"`
	Removed           json.RawMessage `json:"@removed,omitempty"`
}

// Task is a To Do task. Removed is set on a delta item for a deleted task.
type Task struct {
	ID                       string          `json:"id"`
	Title                    string          `json:"title"`
	Status                   string          `json:"status,omitempty"`
	Importance               string          `json:"importance,omitempty"`
	Body                     *ItemBody       `json:"body,omitempty"`
	Categories               []string        `json:"categories,omitempty"`
	IsReminderOn             bool            `json:"isReminderOn,omitempty"`
	ReminderDateTime         *DateTimeZone   `json:"reminderDateTime,omitempty"`
	DueDateTime              *DateTimeZone   `json:"dueDateTime,omitempty"`
	StartDateTime            *DateTimeZone   `json:"startDateTime,omitempty"`
	CompletedDateTime        *DateTimeZone   `json:"completedDateTime,omitempty"`
	Recurrence               json.RawMessage `json:"recurrence,omitempty"`
	HasAttachments           bool            `json:"hasAttachments,omitempty"`
	CreatedDateTime          string          `json:"createdDateTime,omitempty"`
	LastModifiedDateTime     string          `json:"lastModifiedDateTime,omitempty"`
	BodyLastModifiedDateTime string          `json:"bodyLastModifiedDateTime,omitempty"`
	Removed                  json.RawMessage `json:"@removed,omitempty"`
}

// IsNew reports a task that has not changed since its creation. Graph can
// stamp the two times some milliseconds apart.
func (t Task) IsNew() bool {
	c, err1 := time.Parse(time.RFC3339Nano, t.CreatedDateTime)
	m, err2 := time.Parse(time.RFC3339Nano, t.LastModifiedDateTime)
	return err1 == nil && err2 == nil && m.Sub(c) < 2*time.Second
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

// ChecklistItem is a step of a task.
type ChecklistItem struct {
	ID              string `json:"id"`
	DisplayName     string `json:"displayName"`
	IsChecked       bool   `json:"isChecked"`
	CreatedDateTime string `json:"createdDateTime,omitempty"`
	CheckedDateTime string `json:"checkedDateTime,omitempty"`
}

// LinkedResource links a task to an item in another app.
type LinkedResource struct {
	ID              string `json:"id"`
	WebURL          string `json:"webUrl,omitempty"`
	ApplicationName string `json:"applicationName,omitempty"`
	DisplayName     string `json:"displayName,omitempty"`
	ExternalID      string `json:"externalId,omitempty"`
}

// Attachment is a file of a task. ContentBytes is set only on a get of
// one attachment.
type Attachment struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	ContentType          string `json:"contentType,omitempty"`
	Size                 int    `json:"size,omitempty"`
	LastModifiedDateTime string `json:"lastModifiedDateTime,omitempty"`
	ContentBytes         []byte `json:"contentBytes,omitempty"`
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

// Ref names a list, or a task when Task is set.
type Ref struct{ List, Task string }

func (r Ref) listPath() string { return "/me/todo/lists/" + url.PathEscape(r.List) }

func (r Ref) taskPath() string { return r.listPath() + "/tasks/" + url.PathEscape(r.Task) }

// path is the task path, or the list path for a list ref.
func (r Ref) path() string {
	if r.Task == "" {
		return r.listPath()
	}
	return r.taskPath()
}

// TaskResource is the subscription resource of the tasks of a list.
func TaskResource(list string) string { return "/me/todo/lists/" + list + "/tasks" }

// Query narrows a task list. Limit 0 reads every page.
type Query struct {
	Filter  string
	OrderBy string
	Select  string
	Limit   int
}

func (q Query) encode() string {
	v := url.Values{}
	if q.Filter != "" {
		v.Set("$filter", q.Filter)
	}
	if q.OrderBy != "" {
		v.Set("$orderby", q.OrderBy)
	}
	if q.Select != "" {
		v.Set("$select", q.Select)
	}
	if q.Limit > 0 {
		v.Set("$top", strconv.Itoa(min(q.Limit, 100)))
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

// page is one page of a Graph collection.
type page[T any] struct {
	Value     []T    `json:"value"`
	NextLink  string `json:"@odata.nextLink"`
	DeltaLink string `json:"@odata.deltaLink"`
}

// all follows the nextLinks of a collection until limit items (0 is no
// limit). It returns the deltaLink of a delta query.
func all[T any](ctx context.Context, c *Client, path string, limit int) ([]T, string, error) {
	out := []T{}
	for path != "" {
		var p page[T]
		if err := c.Do(ctx, http.MethodGet, path, nil, &p); err != nil {
			return nil, "", err
		}
		out = append(out, p.Value...)
		if limit > 0 && len(out) >= limit {
			return out[:limit], "", nil
		}
		if p.NextLink == "" {
			return out, p.DeltaLink, nil
		}
		path = p.NextLink
	}
	return out, "", nil
}

func get[T any](ctx context.Context, c *Client, path string) (T, error) {
	var out T
	err := c.Do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

func send[T any](ctx context.Context, c *Client, method, path string, body any) (T, error) {
	var out T
	err := c.Do(ctx, method, path, body, &out)
	return out, err
}

func (c *Client) del(ctx context.Context, path string) error {
	return c.Do(ctx, http.MethodDelete, path, nil, nil)
}

// Ping reads one list to check the token.
func (c *Client) Ping(ctx context.Context) error {
	return c.Do(ctx, http.MethodGet, "/me/todo/lists?$top=1", nil, nil)
}

// Lists returns the To Do lists.
func (c *Client) Lists(ctx context.Context) ([]List, error) {
	l, _, err := all[List](ctx, c, "/me/todo/lists", 0)
	return l, err
}

// List returns one list.
func (c *Client) List(ctx context.Context, id string) (List, error) {
	return get[List](ctx, c, Ref{List: id}.listPath())
}

// CreateList creates a list.
func (c *Client) CreateList(ctx context.Context, name string) (List, error) {
	return send[List](ctx, c, http.MethodPost, "/me/todo/lists", map[string]string{"displayName": name})
}

// UpdateList changes a list, for example its displayName.
func (c *Client) UpdateList(ctx context.Context, id string, patch any) (List, error) {
	return send[List](ctx, c, http.MethodPatch, Ref{List: id}.listPath(), patch)
}

// DeleteList deletes a list and its tasks.
func (c *Client) DeleteList(ctx context.Context, id string) error {
	return c.del(ctx, Ref{List: id}.listPath())
}

// ListsDelta runs a delta query on the lists from link, or from the start
// when link is empty. It returns the changes and the next deltaLink.
func (c *Client) ListsDelta(ctx context.Context, link string) ([]List, string, error) {
	if link == "" {
		link = "/me/todo/lists/delta"
	}
	return all[List](ctx, c, link, 0)
}

// Tasks returns the tasks of a list that match q.
func (c *Client) Tasks(ctx context.Context, list string, q Query) ([]Task, error) {
	t, _, err := all[Task](ctx, c, Ref{List: list}.listPath()+"/tasks"+q.encode(), q.Limit)
	return t, err
}

// Task returns one task.
func (c *Client) Task(ctx context.Context, list, id string) (Task, error) {
	return get[Task](ctx, c, Ref{list, id}.taskPath())
}

// CreateTask creates a task from a todoTask body.
func (c *Client) CreateTask(ctx context.Context, list string, body any) (Task, error) {
	return send[Task](ctx, c, http.MethodPost, Ref{List: list}.listPath()+"/tasks", body)
}

// UpdateTask changes the fields of a task in patch.
func (c *Client) UpdateTask(ctx context.Context, list, id string, patch any) (Task, error) {
	return send[Task](ctx, c, http.MethodPatch, Ref{list, id}.taskPath(), patch)
}

// DeleteTask deletes a task.
func (c *Client) DeleteTask(ctx context.Context, list, id string) error {
	return c.del(ctx, Ref{list, id}.taskPath())
}

// Delta runs a delta query on the tasks of a list from link, or from the
// start when link is empty. It returns the changes and the next deltaLink.
func (c *Client) Delta(ctx context.Context, list, link string) ([]Task, string, error) {
	if link == "" {
		link = Ref{List: list}.listPath() + "/tasks/delta"
	}
	return all[Task](ctx, c, link, 0)
}

// ChecklistItems returns the steps of a task.
func (c *Client) ChecklistItems(ctx context.Context, t Ref) ([]ChecklistItem, error) {
	l, _, err := all[ChecklistItem](ctx, c, t.taskPath()+"/checklistItems", 0)
	return l, err
}

// ChecklistItem returns one step.
func (c *Client) ChecklistItem(ctx context.Context, t Ref, id string) (ChecklistItem, error) {
	return get[ChecklistItem](ctx, c, t.taskPath()+"/checklistItems/"+url.PathEscape(id))
}

// CreateChecklistItem adds a step to a task.
func (c *Client) CreateChecklistItem(ctx context.Context, t Ref, body any) (ChecklistItem, error) {
	return send[ChecklistItem](ctx, c, http.MethodPost, t.taskPath()+"/checklistItems", body)
}

// UpdateChecklistItem changes a step.
func (c *Client) UpdateChecklistItem(ctx context.Context, t Ref, id string, patch any) (ChecklistItem, error) {
	return send[ChecklistItem](ctx, c, http.MethodPatch, t.taskPath()+"/checklistItems/"+url.PathEscape(id), patch)
}

// DeleteChecklistItem deletes a step.
func (c *Client) DeleteChecklistItem(ctx context.Context, t Ref, id string) error {
	return c.del(ctx, t.taskPath()+"/checklistItems/"+url.PathEscape(id))
}

// LinkedResources returns the links of a task.
func (c *Client) LinkedResources(ctx context.Context, t Ref) ([]LinkedResource, error) {
	l, _, err := all[LinkedResource](ctx, c, t.taskPath()+"/linkedResources", 0)
	return l, err
}

// LinkedResource returns one link.
func (c *Client) LinkedResource(ctx context.Context, t Ref, id string) (LinkedResource, error) {
	return get[LinkedResource](ctx, c, t.taskPath()+"/linkedResources/"+url.PathEscape(id))
}

// CreateLinkedResource adds a link to a task.
func (c *Client) CreateLinkedResource(ctx context.Context, t Ref, body any) (LinkedResource, error) {
	return send[LinkedResource](ctx, c, http.MethodPost, t.taskPath()+"/linkedResources", body)
}

// UpdateLinkedResource changes a link.
func (c *Client) UpdateLinkedResource(ctx context.Context, t Ref, id string, patch any) (LinkedResource, error) {
	return send[LinkedResource](ctx, c, http.MethodPatch, t.taskPath()+"/linkedResources/"+url.PathEscape(id), patch)
}

// DeleteLinkedResource deletes a link.
func (c *Client) DeleteLinkedResource(ctx context.Context, t Ref, id string) error {
	return c.del(ctx, t.taskPath()+"/linkedResources/"+url.PathEscape(id))
}

// Attachments returns the files of a task, with no content.
func (c *Client) Attachments(ctx context.Context, t Ref) ([]Attachment, error) {
	l, _, err := all[Attachment](ctx, c, t.taskPath()+"/attachments", 0)
	return l, err
}

// Attachment returns one file with its content.
func (c *Client) Attachment(ctx context.Context, t Ref, id string) (Attachment, error) {
	return get[Attachment](ctx, c, t.taskPath()+"/attachments/"+url.PathEscape(id))
}

// AttachmentContent returns the raw bytes of one file.
func (c *Client) AttachmentContent(ctx context.Context, t Ref, id string) ([]byte, error) {
	resp, err := c.Send(ctx, Request{Method: http.MethodGet, Path: t.taskPath() + "/attachments/" + url.PathEscape(id) + "/$value"})
	return resp.Body, err
}

// DeleteAttachment deletes a file.
func (c *Client) DeleteAttachment(ctx context.Context, t Ref, id string) error {
	return c.del(ctx, t.taskPath()+"/attachments/"+url.PathEscape(id))
}

// Attachment size limits of Graph.
const (
	SmallAttachment = 3 << 20  // the largest file for one POST
	MaxAttachment   = 25 << 20 // the largest file of a task
	uploadChunk     = 3 << 20  // under the 4 MB limit of one PUT
)

// AddAttachment adds a file to a task. A file of 3 MB or more goes through
// an upload session.
func (c *Client) AddAttachment(ctx context.Context, t Ref, name, contentType string, data []byte) (Attachment, error) {
	if len(data) > MaxAttachment {
		return Attachment{}, fmt.Errorf("the file has %d bytes; Microsoft To Do takes at most %d", len(data), MaxAttachment)
	}
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	if len(data) < SmallAttachment {
		return send[Attachment](ctx, c, http.MethodPost, t.taskPath()+"/attachments", map[string]any{
			"@odata.type": "#microsoft.graph.taskFileAttachment",
			"name":        name, "contentType": contentType, "size": len(data),
			"contentBytes": base64.StdEncoding.EncodeToString(data),
		})
	}
	return c.upload(ctx, t, name, contentType, data)
}

// upload sends a large file in chunks through an upload session.
func (c *Client) upload(ctx context.Context, t Ref, name, contentType string, data []byte) (Attachment, error) {
	sess, err := send[struct {
		UploadURL string `json:"uploadUrl"`
	}](ctx, c, http.MethodPost, t.taskPath()+"/attachments/createUploadSession", map[string]any{
		"attachmentInfo": map[string]any{"attachmentType": "file", "name": name, "size": len(data), "contentType": contentType},
	})
	if err != nil {
		return Attachment{}, err
	}
	var loc string
	for start := 0; start < len(data); start += uploadChunk {
		end := min(start+uploadChunk, len(data))
		resp, err := c.Send(ctx, Request{
			Method: http.MethodPut, Path: sess.UploadURL + "/content", Body: data[start:end],
			ContentType: "application/octet-stream",
			Headers:     map[string]string{"Content-Range": fmt.Sprintf("bytes %d-%d/%d", start, end-1, len(data))},
		})
		if err != nil {
			c.del(ctx, sess.UploadURL) //nolint:errcheck // the upload error wins
			return Attachment{}, err
		}
		loc = resp.Header.Get("Location")
	}
	if loc == "" {
		return Attachment{}, errors.New("the upload finished with no attachment location")
	}
	id := loc[strings.LastIndex(loc, "/")+1:]
	if i := strings.Index(id, "('"); i >= 0 { // the form Attachments('<id>')
		id = strings.TrimSuffix(id[i+2:], "')")
	}
	return Attachment{ID: id, Name: name, ContentType: contentType, Size: len(data)}, nil
}

// Extensions returns the open extensions of a list or a task.
func (c *Client) Extensions(ctx context.Context, owner Ref) ([]json.RawMessage, error) {
	l, _, err := all[json.RawMessage](ctx, c, owner.path()+"/extensions", 0)
	return l, err
}

// Extension returns one open extension by its name.
func (c *Client) Extension(ctx context.Context, owner Ref, name string) (json.RawMessage, error) {
	return get[json.RawMessage](ctx, c, owner.path()+"/extensions/"+url.PathEscape(name))
}

// CreateExtension adds an open extension with the name and the data.
func (c *Client) CreateExtension(ctx context.Context, owner Ref, name string, data map[string]any) (json.RawMessage, error) {
	body := map[string]any{}
	for k, v := range data {
		body[k] = v
	}
	body["@odata.type"], body["extensionName"] = "microsoft.graph.openTypeExtension", name
	return send[json.RawMessage](ctx, c, http.MethodPost, owner.path()+"/extensions", body)
}

// UpdateExtension changes the data of an open extension.
func (c *Client) UpdateExtension(ctx context.Context, owner Ref, name string, data map[string]any) error {
	body := map[string]any{"@odata.type": "microsoft.graph.openTypeExtension"}
	for k, v := range data {
		body[k] = v
	}
	return c.Do(ctx, http.MethodPatch, owner.path()+"/extensions/"+url.PathEscape(name), body, nil)
}

// DeleteExtension deletes an open extension.
func (c *Client) DeleteExtension(ctx context.Context, owner Ref, name string) error {
	return c.del(ctx, owner.path()+"/extensions/"+url.PathEscape(name))
}

// Subscriptions returns the subscriptions of this app.
func (c *Client) Subscriptions(ctx context.Context) ([]Subscription, error) {
	s, _, err := all[Subscription](ctx, c, "/subscriptions", 0)
	return s, err
}

// Subscription returns one subscription.
func (c *Client) Subscription(ctx context.Context, id string) (Subscription, error) {
	return get[Subscription](ctx, c, "/subscriptions/"+url.PathEscape(id))
}

// CreateSubscription creates a subscription. Graph checks the
// notification URLs before it answers.
func (c *Client) CreateSubscription(ctx context.Context, s Subscription) (Subscription, error) {
	return send[Subscription](ctx, c, http.MethodPost, "/subscriptions", s)
}

// RenewSubscription moves the expiry of a subscription.
func (c *Client) RenewSubscription(ctx context.Context, id string, exp time.Time) (Subscription, error) {
	return send[Subscription](ctx, c, http.MethodPatch, "/subscriptions/"+url.PathEscape(id), map[string]any{"expirationDateTime": exp.UTC()})
}

// DeleteSubscription deletes a subscription. A missing one is not an error.
func (c *Client) DeleteSubscription(ctx context.Context, id string) error {
	err := c.del(ctx, "/subscriptions/"+url.PathEscape(id))
	if IsNotFound(err) {
		return nil
	}
	return err
}
