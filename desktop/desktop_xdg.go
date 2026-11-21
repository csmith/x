//go:build unix && !darwin

package desktop

import (
	"errors"
	"os"
	"path/filepath"
)

var openCommand = []string{"xdg-open"}

var clipboardTools = [][]string{
	{"wl-copy"},
	{"xclip", "-selection", "clipboard"},
	{"xsel", "--clipboard", "--input"},
}

func stateDir() (string, error) {
	return xdgDir("XDG_STATE_HOME", ".local", "state")
}

func dataDir() (string, error) {
	return xdgDir("XDG_DATA_HOME", ".local", "share")
}

func runtimeDir() (string, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return "", errors.New("$XDG_RUNTIME_DIR is not defined")
	}
	if !filepath.IsAbs(dir) {
		return "", errors.New("path in $XDG_RUNTIME_DIR is relative")
	}
	return dir, nil
}

// xdgDir returns the directory the environment variable names, or the
// default under the home directory when it is unset or relative, as the XDG
// base directory specification requires relative paths be ignored.
func xdgDir(env string, fallback ...string) (string, error) {
	if dir := os.Getenv(env); filepath.IsAbs(dir) {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{home}, fallback...)...), nil
}
