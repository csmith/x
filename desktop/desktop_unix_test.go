//go:build unix

package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helperEnv names the file the test binary, launched as a stub command,
// writes its pid and process group to.
const helperEnv = "DESKTOP_TEST_HELPER_OUT"

func TestMain(m *testing.M) {
	if out := os.Getenv(helperEnv); out != "" {
		_ = os.WriteFile(
			out+".tmp",
			fmt.Appendf(nil, "%d %d", os.Getpid(), syscall.Getpgrp()),
			0o600,
		)
		_ = os.Rename(out+".tmp", out)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestLaunch_detachesAndReaps(t *testing.T) {
	out := filepath.Join(t.TempDir(), "report")
	t.Setenv(helperEnv, out)
	// The race detector otherwise holds the stub open for a second on exit.
	t.Setenv("GORACE", "atexit_sleep_ms=0")

	require.NoError(t, Launch(os.Args[0]))

	var report []byte
	require.Eventually(
		t,
		func() bool {
			var err error
			report, err = os.ReadFile(out)
			return err == nil
		},
		10*time.Second,
		10*time.Millisecond,
	)

	var pid, pgrp int
	_, err := fmt.Sscanf(string(report), "%d %d", &pid, &pgrp)
	require.NoError(t, err)

	assert.Equal(t, pid, pgrp, "child should lead its own session and process group")
	assert.NotEqual(t, syscall.Getpgrp(), pgrp)

	// A zombie still answers signal 0; only a reaped process is gone.
	assert.Eventually(
		t,
		func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH },
		10*time.Second,
		10*time.Millisecond,
		"child should be reaped after exiting",
	)
}

func TestLaunch_missingProgram(t *testing.T) {
	require.Error(t, Launch(filepath.Join(t.TempDir(), "missing")))
}
