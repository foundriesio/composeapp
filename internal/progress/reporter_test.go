package progress

import (
	"testing"
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
