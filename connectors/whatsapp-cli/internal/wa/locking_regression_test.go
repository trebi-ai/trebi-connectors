package wa

import (
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// A handler that registers or removes handlers, or calls the client, during
// dispatch must not deadlock.
func TestEventHandlerCanUseClientDuringDispatch(t *testing.T) {
	c, err := New(Options{StorePath: filepath.Join(t.TempDir(), "session.db")})
	if err != nil {
		t.Fatal(err)
	}
	var id uint32
	id = c.AddEventHandler(func(interface{}) {
		c.RemoveEventHandler(c.AddEventHandler(func(interface{}) {}))
		_ = c.IsConnected()
		c.RemoveEventHandler(id)
	})
	done := make(chan struct{})
	go func() {
		c.client.DangerousInternals().DispatchEvent(&events.Connected{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch deadlocked")
	}
}
