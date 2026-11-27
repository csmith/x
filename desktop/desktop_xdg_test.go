//go:build unix && !darwin

package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubTools puts scripts with the given names on an otherwise empty PATH.
// Each appends its name, arguments and standard input to the returned log.
func stubTools(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := "#!/bin/sh\n" +
		"{ echo \"${0##*/} $*\"; while IFS= read -r line || [ -n \"$line\" ]; do echo \"$line\"; done; } >> " + log + "\n"
	for _, name := range names {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700))
	}
	t.Setenv("PATH", dir)
	return log
}

func TestCopy_fallbackOrder(t *testing.T) {
	tests := []struct {
		name      string
		available []string
		want      string
	}{
		{
			"wl-copy first",
			[]string{"wl-copy", "xclip", "xsel"},
			"wl-copy \nhello\n",
		},
		{
			"xclip without wl-copy",
			[]string{"xclip", "xsel"},
			"xclip -selection clipboard\nhello\n",
		},
		{
			"xsel last",
			[]string{"xsel"},
			"xsel --clipboard --input\nhello\n",
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				log := stubTools(t, tt.available...)

				require.NoError(t, Copy("hello"))

				got, err := os.ReadFile(log)
				require.NoError(t, err)
				assert.Equal(t, tt.want, string(got))
			},
		)
	}
}

func TestCopy_fallsBackWhenAToolFails(t *testing.T) {
	log := stubTools(t, "xclip")
	failTool(t, log, "wl-copy")

	require.NoError(t, Copy("hello"))

	got, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.Equal(t, "wl-copy failed\nxclip -selection clipboard\nhello\n", string(got))
}

func TestCopy_allToolsFail(t *testing.T) {
	log := stubTools(t)
	failTool(t, log, "wl-copy")
	failTool(t, log, "xsel")

	err := Copy("hello")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "wl-copy: exit status 1")
	assert.Contains(t, err.Error(), "xsel: exit status 1")
}

// failTool adds a tool beside stubTools' that logs its failure and exits 1.
func failTool(t *testing.T, log, name string) {
	t.Helper()
	script := "#!/bin/sh\necho \"${0##*/} failed\" >> " + log + "\nexit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(log), name), []byte(script), 0o700))
}

func TestCopy_noTool(t *testing.T) {
	stubTools(t)

	err := Copy("hello")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "wl-copy, xclip, xsel")
}

func TestOpen_usesXdgOpen(t *testing.T) {
	log := stubTools(t, "xdg-open")

	require.NoError(t, Open("https://example.com/"))

	assert.Eventually(
		t,
		func() bool {
			got, _ := os.ReadFile(log)
			return strings.HasPrefix(string(got), "xdg-open https://example.com/\n")
		},
		10*time.Second,
		10*time.Millisecond,
	)
}

func TestStateAndDataDir(t *testing.T) {
	tests := []struct {
		env      string
		fn       func() (string, error)
		fallback string
	}{
		{"XDG_STATE_HOME", StateDir, "/home/user/.local/state"},
		{"XDG_DATA_HOME", DataDir, "/home/user/.local/share"},
	}
	for _, tt := range tests {
		t.Run(
			tt.env,
			func(t *testing.T) {
				t.Setenv("HOME", "/home/user")

				t.Run(
					"set",
					func(t *testing.T) {
						t.Setenv(tt.env, "/custom/dir")
						dir, err := tt.fn()
						require.NoError(t, err)
						assert.Equal(t, "/custom/dir", dir)
					},
				)

				t.Run(
					"unset",
					func(t *testing.T) {
						t.Setenv(tt.env, "")
						require.NoError(t, os.Unsetenv(tt.env))
						dir, err := tt.fn()
						require.NoError(t, err)
						assert.Equal(t, tt.fallback, dir)
					},
				)

				t.Run(
					"empty",
					func(t *testing.T) {
						t.Setenv(tt.env, "")
						dir, err := tt.fn()
						require.NoError(t, err)
						assert.Equal(t, tt.fallback, dir)
					},
				)

				t.Run(
					"relative",
					func(t *testing.T) {
						t.Setenv(tt.env, "relative/dir")
						dir, err := tt.fn()
						require.NoError(t, err)
						assert.Equal(t, tt.fallback, dir)
					},
				)
			},
		)
	}
}

func TestRuntimeDir(t *testing.T) {
	t.Run(
		"set",
		func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
			dir, err := RuntimeDir()
			require.NoError(t, err)
			assert.Equal(t, "/run/user/1000", dir)
		},
	)

	t.Run(
		"unset",
		func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", "")
			require.NoError(t, os.Unsetenv("XDG_RUNTIME_DIR"))
			_, err := RuntimeDir()
			require.Error(t, err)
		},
	)

	t.Run(
		"relative",
		func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", "run/user/1000")
			_, err := RuntimeDir()
			require.Error(t, err)
		},
	)
}
