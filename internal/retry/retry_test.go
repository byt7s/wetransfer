package retry

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

type statusErr int

func (e statusErr) Error() string   { return "status" }
func (e statusErr) StatusCode() int { return int(e) }

func TestIsTemporary(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{context.Canceled, false},
		{context.DeadlineExceeded, true},
		{io.EOF, true},
		{io.ErrUnexpectedEOF, true},
		{statusErr(503), true},
		{statusErr(429), true},
		{statusErr(408), true},
		{statusErr(404), false},
		{statusErr(403), false},
		{IncompleteError{Got: 1, Want: 2}, true},
		{errors.New("plain"), false},
	}
	for _, c := range cases {
		if got := IsTemporary(c.err); got != c.want {
			t.Errorf("IsTemporary(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestWatchBudget(t *testing.T) {
	w := NewWatch()
	w.Budget = 10 * time.Millisecond
	if err := w.Arm(); err != nil {
		t.Fatalf("first Arm: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := w.Arm(); err == nil {
		t.Fatal("Arm should fail after the budget")
	}
	w.Mark()
	if err := w.Arm(); err != nil {
		t.Fatalf("Arm after Mark: %v", err)
	}
}

func TestNext(t *testing.T) {
	if got := Next(time.Second, 3*time.Second); got != 2*time.Second {
		t.Errorf("Next(1s) = %v", got)
	}
	if got := Next(2*time.Second, 3*time.Second); got != 3*time.Second {
		t.Errorf("Next(2s) = %v", got)
	}
}

func TestSleepCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sleep(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Errorf("Sleep = %v", err)
	}
}
