// Package file writes files atomically, so readers see either the old
// contents or the new, never a partial write, and finds git projects on
// disk.
package file

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// Write atomically replaces the file at path with data, leaving it with
// exactly perm whatever the umask and whatever mode an existing file had.
// It behaves like WriteFunc.
func Write(path string, data []byte, perm os.FileMode) error {
	return WriteFunc(
		path,
		perm,
		func(w io.Writer) error {
			_, err := w.Write(data)
			return err
		},
	)
}

// WriteFunc atomically replaces the file at path with whatever write writes,
// leaving it with exactly perm whatever the umask and whatever mode an
// existing file had.
//
// The content goes to a temporary file in the same directory, which is
// synced and renamed over path, and then the directory is synced so the
// rename survives a crash (except on Windows, where directories can't be
// synced). If write or any step before the rename fails, the temporary file
// is removed and path is left untouched. If only the directory sync fails,
// path has already been replaced and the error is still returned.
func WriteFunc(path string, perm os.FileMode, write func(io.Writer) error) error {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}

	f, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = f.Close()
			// Windows won't remove the file once it's been made read-only.
			_ = os.Chmod(f.Name(), 0o600)
			_ = os.Remove(f.Name())
		}
	}()

	if err := write(f); err != nil {
		return err
	}
	if err := f.Chmod(perm); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := rename(f.Name(), path); err != nil {
		return err
	}
	renamed = true

	return syncDir(dir)
}

// rename moves from over to. Windows won't replace a read-only file, which
// an earlier write with a perm lacking the owner write bit leaves, so there
// the target is made writable first, and put back if the rename fails.
func rename(from, to string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(from, to)
	}
	info, err := os.Stat(to)
	if err != nil || info.Mode().Perm()&0o200 != 0 {
		return os.Rename(from, to)
	}
	if err := os.Chmod(to, info.Mode().Perm()|0o200); err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		_ = os.Chmod(to, info.Mode().Perm())
		return err
	}
	return nil
}

func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
