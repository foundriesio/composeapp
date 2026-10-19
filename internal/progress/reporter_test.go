package progress

import (
	"context"
	"testing"
	"time"
)

func TestReporter_NoBlocking(t *testing.T) {
	maxProgress := 5
	progressReporter := NewReporter[int](maxProgress + 1)
	expectedProgress := 0

	defer func() {
		progressReporter.Stop(true)
		if expectedProgress != maxProgress {
			t.Errorf("Expected progress: %d, got: %d", maxProgress, expectedProgress)
		}
	}()

	progressReporter.Start(func(p *int) {
		if *p != expectedProgress {
			t.Errorf("Expected progress: %d, got: %d", expectedProgress, *p)
		}
		expectedProgress++
	})

	for i := 0; i < maxProgress; i++ {
		progressReporter.Update(i)
	}
}

func TestReporter_BlockingWithRetry(t *testing.T) {
	maxProgress := 5
	progressReporter := NewReporter[int](1)
	expectedProgress := 0

	defer func() {
		progressReporter.Stop(true)
		if expectedProgress != maxProgress {
			t.Errorf("Expected progress: %d, got: %d", maxProgress, expectedProgress)
		}
	}()

	progressReporter.Start(func(p *int) {
		if *p != expectedProgress {
			t.Errorf("Expected progress: %d, got: %d", expectedProgress, *p)
		}
		expectedProgress++
	})

	for i := 0; i < maxProgress; i++ {
		for !progressReporter.Update(i) {
			// loop until Update(i) returns true
		}
	}
}

// TestReporter_SendWaitsForRoom: with a buffer smaller than the burst and a handler slower than the
// sender, Send loses nothing and Stop(true) returns once every update has been handled.
func TestReporter_SendWaitsForRoom(t *testing.T) {
	r := NewReporter[int](2)
	var got []int
	r.Start(func(p *int) {
		time.Sleep(5 * time.Millisecond)
		got = append(got, *p)
	})
	for i := 0; i < 8; i++ {
		r.Send(context.Background(), i)
	}
	r.Stop(true)
	if len(got) != 8 {
		t.Fatalf("expected 8 updates handled, got %v", got)
	}
	for i, v := range got {
		if v != i {
			t.Fatalf("updates out of order: %v", got)
		}
	}
}

// TestReporter_SendReturnsWhenContextIsDone: a full buffer does not hold a sender whose context is
// cancelled, and a context that is already done never enqueues.
func TestReporter_SendReturnsWhenContextIsDone(t *testing.T) {
	r := NewReporter[int](1)
	release := make(chan struct{})
	var handled int
	r.Start(func(p *int) {
		handled++
		<-release
	})
	ctx, cancel := context.WithCancel(context.Background())
	r.Send(ctx, 0) // taken by the handler, which now blocks
	r.Send(ctx, 1) // fills the buffer
	returned := make(chan struct{})
	go func() {
		r.Send(ctx, 2) // no room: waits for the context
		close(returned)
	}()
	time.Sleep(20 * time.Millisecond)
	select {
	case <-returned:
		t.Fatal("Send returned although the buffer was full and the context live")
	default:
	}
	cancel()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("Send did not return after the context was cancelled")
	}
	r.Send(ctx, 3) // already done: must not block or enqueue
	close(release)
	r.Stop(true)
	if handled != 2 {
		t.Fatalf("expected the handler to see 2 updates, got %d", handled)
	}
}
