// Package desktop starts programs, opens URLs, uses the clipboard and finds
// per-user directories the way the current desktop platform expects.
package desktop

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unicode/utf16"
)

// Launch starts a program detached from this one, in its own session (or
// process group on Windows) with no standard input or output, and returns
// without waiting for it. The program is reaped in the background once it
// exits, so it never lingers as a zombie.
func Launch(name string, args ...string) error {
	c := exec.CommandContext(context.Background(), name, args...)
	c.SysProcAttr = detachedAttr()
	if err := c.Start(); err != nil {
		return err
	}
	go func() { _ = c.Wait() }()
	return nil
}

// Open opens a URL or file with the desktop's default handler, started the
// same detached way as [Launch]. It refuses a url starting with "-", which the
// handler would take as an option; pass a file named that way as "./-name".
func Open(url string) error {
	if strings.HasPrefix(url, "-") {
		return fmt.Errorf("refusing to open %q, which looks like an option", url)
	}
	return Launch(openCommand[0], append(openCommand[1:], url)...)
}

// Copy puts text on the clipboard, waiting for the clipboard tool to finish.
// It tries each installed tool in turn until one succeeds, as a tool can be
// installed but unusable, such as wl-copy in an X11 session.
func Copy(text string) error {
	var errs []error
	for _, tool := range clipboardTools {
		if _, err := exec.LookPath(tool[0]); err != nil {
			continue
		}
		c := exec.CommandContext(context.Background(), tool[0], tool[1:]...)
		c.Stdin = bytes.NewReader(encodeClipboard(text))
		err := c.Run()
		if err == nil {
			return nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", tool[0], err))
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	names := make([]string, len(clipboardTools))
	for i, tool := range clipboardTools {
		names[i] = tool[0]
	}
	return errors.New("no clipboard tool found (install " + strings.Join(names, ", ") + ")")
}

// utf16LE encodes text as UTF-16LE with a byte order mark, which is how
// clip.exe recognises input that isn't in the console's code page.
func utf16LE(text string) []byte {
	units := utf16.Encode([]rune(text))
	out := make([]byte, 0, 2*len(units)+2)
	out = binary.LittleEndian.AppendUint16(out, 0xfeff)
	for _, u := range units {
		out = binary.LittleEndian.AppendUint16(out, u)
	}
	return out
}

// StateDir returns the default directory for user-specific state that should
// persist but isn't worth backing up, such as logs and history. Callers should
// create their own subdirectory within it. It need not exist yet.
//
// On Linux and BSD it is $XDG_STATE_HOME, or ~/.local/state if that is unset
// or relative. On macOS it is ~/Library/Application Support, and on Windows
// %LocalAppData%.
func StateDir() (string, error) {
	return stateDir()
}

// DataDir returns the default directory for user-specific data files.
// Callers should create their own subdirectory within it. It need not exist
// yet.
//
// On Linux and BSD it is $XDG_DATA_HOME, or ~/.local/share if that is unset
// or relative. On macOS it is ~/Library/Application Support, and on Windows
// %AppData%.
func DataDir() (string, error) {
	return dataDir()
}

// RuntimeDir returns the default directory for user-specific runtime files
// such as sockets and pipes. Callers should create their own subdirectory
// within it.
//
// On Linux and BSD it is $XDG_RUNTIME_DIR, and an error if that is unset or
// relative as there is no safe default. On macOS and Windows it is
// [os.TempDir].
func RuntimeDir() (string, error) {
	return runtimeDir()
}
