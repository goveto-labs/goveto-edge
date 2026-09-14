package main

import (
	"context"
	"errors"
	"testing"
)

func TestSkipRewrapAfterDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	persistErr := errors.New("database unavailable")
	if got := skipRewrapAfterDeadline(ctx, persistErr); !errors.Is(got, persistErr) {
		t.Fatalf("live context swallowed persist error: %v", got)
	}
	cancel()
	if got := skipRewrapAfterDeadline(ctx, persistErr); got != nil {
		t.Fatalf("deadline did not skip rewrap error: %v", got)
	}
	if got := skipRewrapAfterDeadline(ctx, nil); got != nil {
		t.Fatalf("nil error after deadline = %v", got)
	}
}
