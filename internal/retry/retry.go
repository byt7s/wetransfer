// Package retry waits out a dropped connection and tries again.
package retry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const WaitNotice = "Connection lost. Resuming when the network is back."

// Watch tracks one transfer attempt. Progress clears it; a stretch of
// failures longer than Budget ends the wait.
type Watch struct {
	Budget   time.Duration
	MaxDelay time.Duration
	Floor    time.Duration

	down time.Time
	told bool
}

func NewWatch() *Watch {
	return &Watch{
		Budget:   12 * time.Minute,
		MaxDelay: 15 * time.Second,
		Floor:    time.Second,
	}
}

// Mark clears the outage clock after progress.
func (w *Watch) Mark() {
	w.down = time.Time{}
	w.told = false
}

// Arm starts the outage clock and fails once the budget is spent.
func (w *Watch) Arm() error {
	budget := w.Budget
	if budget <= 0 {
		budget = 12 * time.Minute
	}
	if w.down.IsZero() {
		w.down = time.Now()
	}
	if time.Since(w.down) >= budget {
		return fmt.Errorf("retry budget exhausted after %s", budget.Truncate(time.Second))
	}
	return nil
}

// Tell runs fn once per outage. Mark arms it again.
func (w *Watch) Tell(fn func()) {
	if fn == nil || w.told {
		return
	}
	w.told = true
	fn()
}

func (w *Watch) Step() time.Duration {
	if w.Floor > 0 {
		return w.Floor
	}
	return time.Second
}

func (w *Watch) Cap() time.Duration {
	if w.MaxDelay > 0 {
		return w.MaxDelay
	}
	return 15 * time.Second
}

// Next returns the next backoff delay, capped by the watch.
func Next(delay, cap time.Duration) time.Duration {
	delay *= 2
	if delay > cap {
		delay = cap
	}
	if delay <= 0 {
		delay = time.Second
	}
	return delay
}

// Sleep waits for d or until the context is canceled.
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// StalledError reports a connection that stopped delivering bytes.
type StalledError struct{}

func (StalledError) Error() string   { return "connection stalled" }
func (StalledError) Timeout() bool   { return true }
func (StalledError) Temporary() bool { return true }

// ErrStalled is returned when no bytes arrive within the idle timeout.
var ErrStalled error = StalledError{}

// IncompleteError means the body ended before the expected byte count. The
// next attempt resumes from the .part file.
type IncompleteError struct {
	Got  int64
	Want int64
}

func (e IncompleteError) Error() string {
	return fmt.Sprintf("incomplete file: %d of %d bytes", e.Got, e.Want)
}

func (e IncompleteError) Temporary() bool { return true }

// IsTemporary reports whether err is worth retrying.
func IsTemporary(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var sc interface{ StatusCode() int }
	if errors.As(err, &sc) {
		code := sc.StatusCode()
		return code == 408 || code == 429 || code >= 500
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var timed interface{ Timeout() bool }
	if errors.As(err, &timed) && timed.Timeout() {
		return true
	}
	var temporary interface{ Temporary() bool }
	if errors.As(err, &temporary) && temporary.Temporary() {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}
