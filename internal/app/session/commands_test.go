package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitConnectedReturnsWhenSocketBecomesReady(t *testing.T) {
	var connected atomic.Bool
	go func() {
		time.Sleep(20 * time.Millisecond)
		connected.Store(true)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitConnected(ctx, connected.Load); err != nil {
		t.Fatalf("waitConnected returned error: %v", err)
	}
}

func TestWaitConnectedHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := waitConnected(ctx, func() bool { return false })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
