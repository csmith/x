//go:build unix

package desktop

import "syscall"

func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func encodeClipboard(text string) []byte {
	return []byte(text)
}
