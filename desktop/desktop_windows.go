package desktop

import (
	"errors"
	"os"
	"syscall"
)

// detachedProcess is DETACHED_PROCESS, which the syscall package lacks.
const detachedProcess = 0x00000008

func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP}
}

var openCommand = []string{"rundll32", "url.dll,FileProtocolHandler"}

var clipboardTools = [][]string{{"clip.exe"}}

// clip.exe reads UTF-8 as the console's code page, garbling non-ASCII text.
func encodeClipboard(text string) []byte {
	return utf16LE(text)
}

func stateDir() (string, error) {
	return envDir("LocalAppData")
}

func dataDir() (string, error) {
	return envDir("AppData")
}

func runtimeDir() (string, error) {
	return os.TempDir(), nil
}

func envDir(env string) (string, error) {
	dir := os.Getenv(env)
	if dir == "" {
		return "", errors.New("%" + env + "% is not defined")
	}
	return dir, nil
}
