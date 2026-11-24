package balance

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTryPollNowBusy(t *testing.T) {
	p := New(nil, nil, Options{Interval: time.Minute})
	p.running <- struct{}{} // a pass is running
	if _, err := p.TryPollNow(context.Background(), "org"); !errors.Is(err, ErrBusy) {
		t.Fatalf("TryPollNow during a pass: %v, want ErrBusy", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.PollNow(ctx, "org"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("PollNow waiting past ctx: %v, want deadline exceeded", err)
	}
}
