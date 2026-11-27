package temporal

import (
	"context"
	"fmt"
)

// Runner runs a worker until interruptCh receives, as [worker.Worker] does.
//
// [worker.Worker]: https://pkg.go.dev/go.temporal.io/sdk/worker#Worker
type Runner interface {
	Run(interruptCh <-chan any) error
}

// RunWorker runs w until ctx is done or w stops itself, such as when a poll
// keeps failing with a persistent error. When ctx is done, w is stopped,
// draining as its options say, and RunWorker returns nil. If w fails to
// start, or stops itself with an error, RunWorker returns that error.
//
// RunWorker stops w before returning, so callers must not call Stop. A worker
// can't be started again after an error, so a caller that retries needs a
// new worker.
func RunWorker(ctx context.Context, w Runner) error {
	interrupt := make(chan any)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			select {
			case interrupt <- context.Cause(ctx):
			case <-done:
			}
		case <-done:
		}
	}()

	if err := w.Run(interrupt); err != nil {
		return fmt.Errorf("running temporal worker: %w", err)
	}
	return nil
}
