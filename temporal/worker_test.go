package temporal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/worker"
)

var _ Runner = worker.Worker(nil)

func TestRunWorker_returnsNilWhenContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	w := &fakeRunner{stopped: make(chan error)}
	result := runWorkerAsync(ctx, w)

	cancel()
	require.NoError(t, waitFor(t, result))
	assert.True(t, w.interrupted)
}

func TestRunWorker_returnsFatalStopError(t *testing.T) {
	fatal := errors.New("namespace not found")
	w := &fakeRunner{stopped: make(chan error, 1)}
	w.stopped <- fatal

	err := RunWorker(t.Context(), w)
	require.ErrorIs(t, err, fatal)
	assert.False(t, w.interrupted)
}

func TestRunWorker_returnsStartError(t *testing.T) {
	startErr := errors.New("bad options")
	w := &fakeRunner{startErr: startErr}

	assert.ErrorIs(t, RunWorker(t.Context(), w), startErr)
}

type fakeRunner struct {
	startErr    error
	stopped     chan error
	interrupted bool
}

func (f *fakeRunner) Run(interruptCh <-chan any) error {
	if f.startErr != nil {
		return f.startErr
	}
	select {
	case <-interruptCh:
		f.interrupted = true
		return nil
	case err := <-f.stopped:
		return err
	}
}

func runWorkerAsync(ctx context.Context, w Runner) <-chan error {
	result := make(chan error, 1)
	go func() { result <- RunWorker(ctx, w) }()
	return result
}

func waitFor(t *testing.T, result <-chan error) error {
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		require.FailNow(t, "RunWorker didn't return")
		return nil
	}
}
