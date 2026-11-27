package desktop

import (
	"os"
	"path/filepath"
)

var openCommand = []string{"open"}

var clipboardTools = [][]string{
	{"pbcopy"},
}

func stateDir() (string, error) {
	return applicationSupport()
}

func dataDir() (string, error) {
	return applicationSupport()
}

func runtimeDir() (string, error) {
	return os.TempDir(), nil
}

func applicationSupport() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support"), nil
}
