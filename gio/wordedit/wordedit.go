// Package wordedit adds Ctrl-modified word motion and deletion to Gio's
// widget.Editor, which on Linux takes those keys as shortcuts and drops them.
package wordedit

import (
	"unicode"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"
)

// Update handles word editing keys for ed: Ctrl+Left and Ctrl+Right move the
// caret by a word, extending the selection with Shift, and Ctrl+Backspace and
// Ctrl+Delete delete a word, or the selection if there is one. Deletions are
// ignored while ed is read-only, but motion still works. Edits go through the
// editor's caret and delete calls, so they can be undone.
//
// Update must be called each frame before anything else reads ed's events,
// including ed's own Update and Layout, since Gio gives each key event to the
// first filter that matches it. It reports whether it changed ed's text, as
// the editor won't send a ChangeEvent for these edits.
func Update(gtx layout.Context, ed *widget.Editor) (changed bool) {
	for {
		ev, ok := gtx.Event(
			key.Filter{Focus: ed, Name: key.NameLeftArrow, Required: key.ModCtrl, Optional: key.ModShift},
			key.Filter{Focus: ed, Name: key.NameRightArrow, Required: key.ModCtrl, Optional: key.ModShift},
			key.Filter{Focus: ed, Name: key.NameDeleteBackward, Required: key.ModCtrl, Optional: key.ModShift},
			key.Filter{Focus: ed, Name: key.NameDeleteForward, Required: key.ModCtrl, Optional: key.ModShift},
		)
		if !ok {
			return changed
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		switch ke.Name {
		case key.NameLeftArrow:
			moveWord(ed, -1, ke.Modifiers.Contain(key.ModShift))
		case key.NameRightArrow:
			moveWord(ed, 1, ke.Modifiers.Contain(key.ModShift))
		case key.NameDeleteBackward:
			changed = deleteWord(ed, -1) || changed
		case key.NameDeleteForward:
			changed = deleteWord(ed, 1) || changed
		}
	}
}

// Boundary returns the rune offset reached by moving a word from pos in
// direction dir (-1 back, 1 forward) through text: past any whitespace, then
// past the run of non-whitespace beyond it.
func Boundary(text []rune, pos, dir int) int {
	skip := func(space bool) {
		for {
			// The rune a step in dir crosses: the one at pos going forward,
			// the one before it going back.
			i := pos
			if dir < 0 {
				i--
			}
			if i < 0 || i >= len(text) || unicode.IsSpace(text[i]) != space {
				return
			}
			pos += dir
		}
	}
	skip(true)
	skip(false)
	return pos
}

func moveWord(ed *widget.Editor, dir int, extend bool) {
	caret, anchor := ed.Selection()
	pos := Boundary([]rune(ed.Text()), caret, dir)
	if extend {
		ed.SetCaret(pos, anchor)
	} else {
		ed.SetCaret(pos, pos)
	}
}

func deleteWord(ed *widget.Editor, dir int) bool {
	if ed.ReadOnly {
		return false
	}
	if ed.SelectionLen() > 0 {
		return ed.Delete(1) != 0
	}
	caret, _ := ed.Selection()
	pos := Boundary([]rune(ed.Text()), caret, dir)
	if pos == caret {
		// Delete on an empty selection removes a character, so a caret already
		// at the text's edge must not reach it.
		return false
	}
	// Selecting the word first makes Delete remove it as one undoable edit.
	ed.SetCaret(caret, pos)
	return ed.Delete(1) != 0
}
