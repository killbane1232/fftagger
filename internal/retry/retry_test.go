package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFiveRetriesWithOneSecondPauses(t *testing.T) {
	t.Parallel()
	want := errors.New("temporary failure")
	var calls []time.Time
	err := Do(context.Background(), func() error {
		calls = append(calls, time.Now())
		return want
	})
	if !errors.Is(err, want) || len(calls) != 6 {
		t.Fatalf("calls=%d error=%v", len(calls), err)
	}
	for i := 1; i < len(calls); i++ {
		if calls[i].Sub(calls[i-1]) < time.Second {
			t.Fatal("retry did not pause for one second")
		}
	}
}

func TestPermanentAndCancellationStopRetries(t *testing.T) {
	want := errors.New("cannot repeat")
	calls := 0
	err := Do(context.Background(), func() error { calls++; return Permanent(want) })
	if calls != 1 || !errors.Is(err, want) {
		t.Fatalf("permanent failure retried: %d %v", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Do(ctx, func() error { close(started); return want })
	}()
	<-started
	cancel()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("cancelled retry waited for the full pause")
	}
}
