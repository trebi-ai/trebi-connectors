package app

import (
	"errors"
	"testing"
	"time"
)

func TestReconnectTrackerBackoffAndExhaust(t *testing.T) {
	tr := reconnectTracker{MaxDuration: 10 * time.Second}
	tr.noteDisconnect()

	d1 := tr.nextDelay()
	if d1 != reconnectMinDelay {
		t.Fatalf("first delay: got %v want %v", d1, reconnectMinDelay)
	}
	d2 := tr.nextDelay()
	if d2 != 2*reconnectMinDelay {
		t.Fatalf("second delay: got %v want %v", d2, 2*reconnectMinDelay)
	}
	d3 := tr.nextDelay()
	if d3 != 4*reconnectMinDelay {
		t.Fatalf("third delay: got %v want %v", d3, 4*reconnectMinDelay)
	}

	// Cap at max delay.
	for i := 0; i < 10; i++ {
		_ = tr.nextDelay()
	}
	dCap := tr.nextDelay()
	if dCap != reconnectMaxDelay {
		// may be clamped by remaining budget if MaxDuration is small
		if dCap > reconnectMaxDelay {
			t.Fatalf("delay %v exceeds max %v", dCap, reconnectMaxDelay)
		}
	}

	tr.reset()
	if !tr.started.IsZero() || tr.attempt != 0 {
		t.Fatalf("reset failed: %+v", tr)
	}
}

func TestReconnectTrackerExhausted(t *testing.T) {
	tr := reconnectTracker{MaxDuration: 50 * time.Millisecond}
	tr.noteDisconnect()
	if tr.exhausted() {
		t.Fatal("should not be exhausted immediately")
	}
	time.Sleep(60 * time.Millisecond)
	if !tr.exhausted() {
		t.Fatal("expected exhausted after MaxDuration")
	}
}

func TestReconnectTrackerUnlimited(t *testing.T) {
	tr := reconnectTracker{MaxDuration: 0}
	tr.noteDisconnect()
	time.Sleep(20 * time.Millisecond)
	if tr.exhausted() {
		t.Fatal("unlimited tracker must never exhaust")
	}
}

func TestErrReconnectExhaustedIs(t *testing.T) {
	wrapped := errors.Join(errReconnectExhausted, errors.New("after 5m0s"))
	if !errors.Is(wrapped, errReconnectExhausted) {
		t.Fatal("errors.Is should match errReconnectExhausted")
	}
}
