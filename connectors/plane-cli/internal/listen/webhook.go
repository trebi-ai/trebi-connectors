package listen

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
)

// Receiver is the HTTP handler for Plane webhook deliveries. It checks the
// X-Plane-Signature HMAC, drops repeated event ids, and writes each event.
type Receiver struct {
	projects *Projects
	out      Sink
	filter   Filter
	// ProjectID, when set, drops events of other projects.
	ProjectID string

	mu     sync.Mutex
	secret []byte
	seen   map[string]bool
}

// NewReceiver writes accepted events to out.
func NewReceiver(projects *Projects, out Sink, filter Filter) *Receiver {
	return &Receiver{projects: projects, out: out, filter: filter, seen: map[string]bool{}}
}

// SetSecret sets the webhook secret. Deliveries fail with 503 until it is set.
func (r *Receiver) SetSecret(s string) {
	r.mu.Lock()
	r.secret = []byte(s)
	r.mu.Unlock()
}

const maxPayload = 10 << 20

func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		// The tunnel check and health probes use GET.
		fmt.Fprintln(w, "ok")
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxPayload))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	secret := r.secret
	r.mu.Unlock()
	if len(secret) == 0 {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	if !validSignature(secret, body, req.Header.Get("X-Plane-Signature")) {
		fmt.Fprintln(os.Stderr, "listen: rejected a delivery with a bad signature")
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	// Answer 200 for each signed delivery: Plane does not retry a 4xx, and failures can disable the webhook.
	w.WriteHeader(http.StatusOK)
	ev, err := decodePayload(body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: cannot read a delivery: %v\n", err)
		return
	}
	if r.duplicate(ev.EventID) {
		return
	}
	ev.Source = "webhook"
	// Some payloads have an enum name ("WebhookScope.ScopeChoices.WORK_ITEM_UPDATED"); the header has the dot name.
	h := req.Header.Get("X-Plane-Event")
	if (!strings.Contains(ev.Event, ".") || strings.HasPrefix(ev.Event, "WebhookScope.")) && h != "" && !strings.HasPrefix(h, "WebhookScope.") {
		ev.Event = h
	}
	if !r.filter.Match(ev.Event) {
		return
	}
	// A delete event has the record in previous_attributes.
	obj := ev.Data
	if obj == nil {
		obj = ev.PreviousAttributes
	}
	if r.ProjectID != "" {
		if pid, ok := obj["project_id"].(string); ok && pid != r.ProjectID {
			return
		}
	}
	if strings.HasPrefix(ev.Event, "workitem.") && ev.EntityType == "issue" {
		ev.Key = r.projects.workItemKey(context.Background(), obj)
	}
	if err := r.out.Emit(ev); err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
	}
}

func (r *Receiver) duplicate(eventID string) bool {
	if eventID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen[eventID] {
		return true
	}
	r.seen[eventID] = true
	return false
}

func validSignature(secret, body []byte, sig string) bool {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(strings.TrimPrefix(strings.TrimSpace(sig), "sha256=")))
}

// decodePayload reads a v2 payload. Plane sometimes sends data or
// previous_attributes as a JSON string ("{}") in place of an object.
func decodePayload(body []byte) (Event, error) {
	var raw struct {
		Event
		Data               json.RawMessage `json:"data"`
		PreviousAttributes json.RawMessage `json:"previous_attributes"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Event{}, err
	}
	ev := raw.Event
	var err error
	if ev.Data, err = objectField(raw.Data); err != nil {
		return Event{}, fmt.Errorf("data: %w", err)
	}
	if ev.PreviousAttributes, err = objectField(raw.PreviousAttributes); err != nil {
		return Event{}, fmt.Errorf("previous_attributes: %w", err)
	}
	return ev, nil
}

func objectField(raw json.RawMessage) (map[string]any, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = json.RawMessage(s)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, nil
	}
	return m, nil
}
