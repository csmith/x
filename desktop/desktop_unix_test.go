//go:build unix

package desktop

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helperEnv names the file the test binary, launched as a stub command,
// reports to: its pid, process group, and which of its standard streams are
// /dev/null.
const helperEnv = "DESKTOP_TEST_HELPER_OUT"

// echoEnv makes the stub also copy its input to its output and write "oops"
// to its error output.
const echoEnv = "DESKTOP_TEST_HELPER_ECHO"

func TestMain(m *testing.M) {
	if out := os.Getenv(helperEnv); out != "" {
		if os.Getenv(echoEnv) != "" {
			_, _ = io.Copy(os.Stdout, os.Stdin)
			_, _ = io.WriteString(os.Stderr, "oops")
		}
		_ = os.WriteFile(
			out+".tmp",
			fmt.Appendf(
				nil,
				"%d %d %t %t %t",
				os.Getpid(),
				syscall.Getpgrp(),
				isDevNull(os.Stdin),
				isDevNull(os.Stdout),
				isDevNull(os.Stderr),
			),
			0o600,
		)
		_ = os.Rename(out+".tmp", out)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func isDevNull(f *os.File) bool {
	got, err := f.Stat()
	if err != nil {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err == nil && os.SameFile(got, null)
}

type helperReport struct {
	pid, pgrp                int
	nullIn, nullOut, nullErr bool
}

// launchHelper launches the test binary as a stub command, checks it was
// detached and reaped, and returns its report.
func launchHelper(t *testing.T, opts ...LaunchOption) helperReport {
	t.Helper()
	out := filepath.Join(t.TempDir(), "report")
	t.Setenv(helperEnv, out)
	// The race detector otherwise holds the stub open for a second on exit.
	t.Setenv("GORACE", "atexit_sleep_ms=0")

	require.NoError(t, Launch(os.Args[0], nil, opts...))

	var raw []byte
	require.Eventually(
		t,
		func() bool {
			var err error
			raw, err = os.ReadFile(out)
			return err == nil
		},
		10*time.Second,
		10*time.Millisecond,
	)

	var r helperReport
	_, err := fmt.Sscanf(string(raw), "%d %d %t %t %t", &r.pid, &r.pgrp, &r.nullIn, &r.nullOut, &r.nullErr)
	require.NoError(t, err)

	assert.Equal(t, r.pid, r.pgrp, "child should lead its own session and process group")
	assert.NotEqual(t, syscall.Getpgrp(), r.pgrp)

	// A zombie still answers signal 0; only a reaped process is gone.
	assert.Eventually(
		t,
		func() bool { return syscall.Kill(r.pid, 0) == syscall.ESRCH },
		10*time.Second,
		10*time.Millisecond,
		"child should be reaped after exiting",
	)
	return r
}

func TestLaunch_detachesAndReapsWithStreamsUnconnected(t *testing.T) {
	r := launchHelper(t)

	assert.True(t, r.nullIn, "stdin")
	assert.True(t, r.nullOut, "stdout")
	assert.True(t, r.nullErr, "stderr")
}

// syncBuffer is written by the goroutine copying a program's output while the
// test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestLaunch_connectsStreams(t *testing.T) {
	t.Setenv(echoEnv, "1")
	var stdout, stderr syncBuffer

	r := launchHelper(t, WithStdin(strings.NewReader("hello")), WithStdout(&stdout), WithStderr(&stderr))

	assert.False(t, r.nullIn, "stdin")
	assert.False(t, r.nullOut, "stdout")
	assert.False(t, r.nullErr, "stderr")
	assert.Eventually(t, func() bool { return stdout.String() == "hello" }, 10*time.Second, 10*time.Millisecond)
	assert.Eventually(t, func() bool { return stderr.String() == "oops" }, 10*time.Second, 10*time.Millisecond)
}

func TestLaunch_missingProgram(t *testing.T) {
	require.Error(t, Launch(filepath.Join(t.TempDir(), "missing"), nil))
}
