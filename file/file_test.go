package file

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrite_createsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")

	require.NoError(t, Write(path, []byte("hello"), 0o644))

	assertContents(t, path, "hello")
	assertOnlyFiles(t, filepath.Dir(path), "out.txt")
}

func TestWrite_replacesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	require.NoError(t, os.WriteFile(path, []byte("old contents that are longer"), 0o644))

	require.NoError(t, Write(path, []byte("new"), 0o644))

	assertContents(t, path, "new")
	assertOnlyFiles(t, filepath.Dir(path), "out.txt")
}

func TestWrite_relativePath(t *testing.T) {
	t.Chdir(t.TempDir())

	require.NoError(t, Write("out.txt", []byte("hello"), 0o644))

	assertContents(t, "out.txt", "hello")
	assertOnlyFiles(t, ".", "out.txt")
}

func TestWrite_replacesReadOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	require.NoError(t, Write(path, []byte("old"), 0o444))

	require.NoError(t, Write(path, []byte("new"), 0o644))

	assertContents(t, path, "new")
	assertOnlyFiles(t, filepath.Dir(path), "out.txt")
}

func TestWrite_setsExactPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows only has a read-only attribute")
	}

	tests := map[string]struct {
		existing os.FileMode
		perm     os.FileMode
	}{
		"a new file":                 {0, 0o640},
		"a more permissive existing": {0o666, 0o600},
		"a less permissive existing": {0o600, 0o644},
		"a read-only target":         {0o644, 0o400},
	}
	for name, tt := range tests {
		t.Run(
			name,
			func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "out.txt")
				if tt.existing != 0 {
					require.NoError(t, os.WriteFile(path, []byte("old"), tt.existing))
					require.NoError(t, os.Chmod(path, tt.existing))
				}

				require.NoError(t, Write(path, []byte("new"), tt.perm))

				info, err := os.Stat(path)
				require.NoError(t, err)
				assert.Equal(t, tt.perm, info.Mode().Perm())
				assertContents(t, path, "new")
			},
		)
	}
}

func TestWriteFunc_streamsContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")

	err := WriteFunc(
		path,
		0o644,
		func(w io.Writer) error {
			for _, part := range []string{"one ", "two ", "three"} {
				if _, err := io.WriteString(w, part); err != nil {
					return err
				}
			}
			return nil
		},
	)
	require.NoError(t, err)

	assertContents(t, path, "one two three")
	assertOnlyFiles(t, filepath.Dir(path), "out.txt")
}

func TestWriteFunc_failurePartWayLeavesTargetUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o644))
	failure := errors.New("generator failed")

	err := WriteFunc(
		path,
		0o644,
		func(w io.Writer) error {
			if _, err := io.WriteString(w, "partial new contents"); err != nil {
				return err
			}
			return failure
		},
	)

	require.ErrorIs(t, err, failure)
	assertContents(t, path, "old")
	assertOnlyFiles(t, filepath.Dir(path), "out.txt")
}

func TestWriteFunc_failurePartWayCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	failure := errors.New("generator failed")

	err := WriteFunc(
		filepath.Join(dir, "out.txt"),
		0o644,
		func(w io.Writer) error {
			_, _ = io.WriteString(w, "partial")
			return failure
		},
	)

	require.ErrorIs(t, err, failure)
	assertOnlyFiles(t, dir)
}

func TestWriteFunc_panicRemovesTempFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o644))

	assert.Panics(
		t,
		func() {
			_ = WriteFunc(
				path,
				0o644,
				func(w io.Writer) error {
					_, _ = io.WriteString(w, "partial")
					panic("boom")
				},
			)
		},
	)

	assertContents(t, path, "old")
	assertOnlyFiles(t, filepath.Dir(path), "out.txt")
}

func TestWrite_missingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "out.txt")

	assert.ErrorIs(t, Write(path, []byte("hello"), 0o644), os.ErrNotExist)
}

func TestWrite_targetIsDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out")
	require.NoError(t, os.Mkdir(path, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(path, "keep"), nil, 0o644))

	require.Error(t, Write(path, []byte("hello"), 0o644))
	assertOnlyFiles(t, dir, "out")
}

func assertContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, want, string(got))
}

func assertOnlyFiles(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, want, names)
}
