package app

import (
	"fmt"
	"sync"
	"time"
)

// decryptFailLimit failures in decryptFailWindow set the decrypt error.
// One failure is often not a lost message: whatsmeow asks for a resend.
const (
	decryptFailLimit  = 3
	decryptFailWindow = 30 * time.Minute
)

// decryptWatch counts the messages that do not decrypt. Run writes it and
// AuthStatus reads it, so a mutex guards it.
type decryptWatch struct {
	now func() time.Time

	mu    sync.Mutex
	fails []time.Time
	msg   string
}

func newDecryptWatch(now func() time.Time) *decryptWatch {
	return &decryptWatch{now: now}
}

// Fail records one failure in chat. It returns true when the decrypt error
// starts.
func (w *decryptWatch) Fail(chat string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	keep := w.fails[:0]
	for _, t := range w.fails {
		if now.Sub(t) < decryptFailWindow {
			keep = append(keep, t)
		}
	}
	w.fails = append(keep, now)
	if w.msg != "" || len(w.fails) < decryptFailLimit {
		return false
	}
	w.msg = fmt.Sprintf("WhatsApp messages cannot be read on this linked device (%d in the last %d minutes, last in %s). Link the phone again if this does not stop.",
		len(w.fails), int(decryptFailWindow.Minutes()), chat)
	return true
}

// Clear forgets the failures. It returns true when the decrypt error was
// active.
func (w *decryptWatch) Clear() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	was := w.msg != ""
	w.fails, w.msg = nil, ""
	return was
}

// Active returns the status message while the decrypt error holds.
func (w *decryptWatch) Active() (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.msg, w.msg != ""
}
