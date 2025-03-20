package progress

import (
	"sync"
)

type (
	Reporter[T any] interface {
		Start(progressHandler func(*T))
		Update(progressValue T) bool
		Stop(drainChannel bool)
	}

	reporterImpl[T any] struct {
		progressCh chan T
		doneCh     chan struct{}
		wg         sync.WaitGroup
		once       sync.Once
	}
)

func NewReporter[T any](bufferSize int) Reporter[T] {
	return &reporterImpl[T]{
		progressCh: make(chan T, bufferSize),
		doneCh:     make(chan struct{}),
	}
}

// Start starts the worker to handle progress updates;
// it invokes the specified progress handler for each progress update in a separate goroutine
func (sr *reporterImpl[T]) Start(progressHandler func(*T)) {
	if progressHandler == nil {
		panic("progressHandler specified in the Start method is nil; it should be a non-nil function")
	}
	sr.wg.Add(1)

	go func() {
		defer sr.wg.Done()
		for {
			select {
			case progress, ok := <-sr.progressCh:
				if !ok {
					return // The progress channel is drained and closed, exit the goroutine
				}
				progressHandler(&progress) // Invoke the progress handler
			case <-sr.doneCh:
				return // Stop/cancel signal received, exit the goroutine
			}
		}
	}()
}

// Update attempts to send a progress update (non-blocking).
// Returns true if successfully queued, false if dropped due to a full buffer.
func (sr *reporterImpl[T]) Update(progressValue T) bool {
	select {
	case sr.progressCh <- progressValue:
		return true // Successfully queued
	default:
		return false // Dropped due to full buffer
	}
}

// Stop stops the worker and closes the channel
func (sr *reporterImpl[T]) Stop(drainChannel bool) {
	sr.once.Do(func() {
		close(sr.progressCh)
		if !drainChannel {
			// If the channel does not need to be drained, close the done channel to signal the worker to exit
			close(sr.doneCh)
		}
		// Wait for the worker to exit
		sr.wg.Wait()
	})
}
