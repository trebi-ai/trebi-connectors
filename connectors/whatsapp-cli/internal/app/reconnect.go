package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/wa"
)

const (
	reconnectMinDelay = 2 * time.Second
	reconnectMaxDelay = 30 * time.Second
)

// errReconnectExhausted is returned when MaxReconnect is exceeded.
var errReconnectExhausted = errors.New("reconnect exhausted")

// reconnectTracker bounds reconnect attempts across flapping disconnects.
// MaxDuration is measured from the first disconnect of an outage until a
// successful Connected event resets the tracker. 0 means unlimited.
type reconnectTracker struct {
	MaxDuration time.Duration

	started time.Time
	attempt int
}

func (t *reconnectTracker) reset() {
	t.started = time.Time{}
	t.attempt = 0
}

func (t *reconnectTracker) noteDisconnect() {
	if t.started.IsZero() {
		t.started = time.Now()
	}
}

func (t *reconnectTracker) exhausted() bool {
	return t.MaxDuration > 0 && !t.started.IsZero() && time.Since(t.started) >= t.MaxDuration
}

func (t *reconnectTracker) nextDelay() time.Duration {
	t.attempt++
	delay := reconnectMinDelay
	for i := 1; i < t.attempt; i++ {
		delay *= 2
		if delay >= reconnectMaxDelay {
			delay = reconnectMaxDelay
			break
		}
	}
	if t.MaxDuration > 0 {
		rem := t.MaxDuration - time.Since(t.started)
		if rem < delay {
			if rem < 0 {
				return 0
			}
			return rem
		}
	}
	return delay
}

// reconnectOnce waits with exponential backoff, then tries to connect once.
//
// Returns:
//   - nil if already connected or Connect succeeded
//   - ctx.Err() if cancelled
//   - errReconnectExhausted (wrapped) if MaxDuration elapsed
//   - a Connect error if the attempt failed (caller may retry)
func (a *App) reconnectOnce(ctx context.Context, t *reconnectTracker) error {
	t.noteDisconnect()
	if t.exhausted() {
		return fmt.Errorf("%w after %s", errReconnectExhausted, t.MaxDuration)
	}

	delay := t.nextDelay()
	fmt.Fprintf(os.Stderr, "Reconnecting in %s (attempt %d)...\n", delay, t.attempt)

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}

	if t.exhausted() {
		return fmt.Errorf("%w after %s", errReconnectExhausted, t.MaxDuration)
	}
	if a.wa.IsConnected() {
		return nil
	}

	if err := a.wa.Connect(ctx, wa.ConnectOptions{AllowQR: false}); err != nil {
		fmt.Fprintf(os.Stderr, "Reconnect failed: %v\n", err)
		return err
	}
	return nil
}

// runReconnectAttempts keeps calling reconnectOnce until connect succeeds,
// the context is cancelled, or the reconnect budget is exhausted.
func (a *App) runReconnectAttempts(ctx context.Context, t *reconnectTracker) error {
	for {
		err := a.reconnectOnce(ctx, t)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errReconnectExhausted) {
			return err
		}
		// Connect failed; loop for another backoff attempt.
	}
}
