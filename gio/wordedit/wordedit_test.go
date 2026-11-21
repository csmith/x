package wordedit

import (
	"image"
	"testing"

	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBoundary(t *testing.T) {
	tests := []struct {
		name string
		text string
		pos  int
		dir  int
		want int
	}{
		{"back from word end to its start", "buy milk today", 8, -1, 4},
		{"back from word middle to its start", "buy milk today", 6, -1, 4},
		{"back over space and word", "buy milk today", 9, -1, 4},
		{"back from word start to previous word", "buy milk today", 4, -1, 0},
		{"back at start stays", "buy milk today", 0, -1, 0},
		{"forward from word middle to its end", "buy milk today", 6, 1, 8},
		{"forward from word start to its end", "buy milk today", 4, 1, 8},
		{"forward over space and word", "buy milk today", 8, 1, 14},
		{"forward at end stays", "buy milk today", 14, 1, 14},
		{"back over trailing spaces and word", "buy  ", 5, -1, 0},
		{"forward stops before trailing spaces", "buy  ", 0, 1, 3},
		{"back over a newline", "buy\nmilk", 4, -1, 0},
		{"punctuation is part of a word", "don't stop", 0, 1, 5},
		{"multi-byte runes count once", "café au lait", 0, 1, 4},
		{"empty text", "", 0, -1, 0},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				assert.Equal(t, tt.want, Boundary([]rune(tt.text), tt.pos, tt.dir))
			},
		)
	}
}

func TestUpdateMovesByWord(t *testing.T) {
	tests := []struct {
		name       string
		caret      int
		anchor     int
		key        key.Name
		mods       key.Modifiers
		wantCaret  int
		wantAnchor int
	}{
		{"left", 8, 8, key.NameLeftArrow, key.ModCtrl, 4, 4},
		{"right", 4, 4, key.NameRightArrow, key.ModCtrl, 8, 8},
		{"left collapses a selection", 6, 2, key.NameLeftArrow, key.ModCtrl, 4, 4},
		{"shift left selects", 8, 8, key.NameLeftArrow, key.ModCtrl | key.ModShift, 4, 8},
		{"shift right selects", 4, 4, key.NameRightArrow, key.ModCtrl | key.ModShift, 8, 4},
		{"shift extends a selection", 6, 2, key.NameRightArrow, key.ModCtrl | key.ModShift, 8, 2},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				h := newHarness(t)
				h.ed.SetCaret(tt.caret, tt.anchor)
				changed := h.press(tt.key, tt.mods)
				assert.False(t, changed)
				caret, anchor := h.ed.Selection()
				assert.Equal(t, tt.wantCaret, caret)
				assert.Equal(t, tt.wantAnchor, anchor)
				assert.Equal(t, "buy milk today", h.ed.Text())
			},
		)
	}
}

func TestUpdateDeletesByWord(t *testing.T) {
	tests := []struct {
		name        string
		caret       int
		anchor      int
		key         key.Name
		want        string
		wantChanged bool
	}{
		{"backspace deletes the word before", 14, 14, key.NameDeleteBackward, "buy milk ", true},
		{"backspace from word middle", 6, 6, key.NameDeleteBackward, "buy lk today", true},
		{"backspace over space and word", 9, 9, key.NameDeleteBackward, "buy today", true},
		{"backspace at start does nothing", 0, 0, key.NameDeleteBackward, "buy milk today", false},
		{"delete removes the word after", 0, 0, key.NameDeleteForward, " milk today", true},
		{"delete over space and word", 3, 3, key.NameDeleteForward, "buy today", true},
		{"delete at end does nothing", 14, 14, key.NameDeleteForward, "buy milk today", false},
		{"backspace removes only a selection", 4, 7, key.NameDeleteBackward, "buy k today", true},
		{"delete removes only a selection", 4, 7, key.NameDeleteForward, "buy k today", true},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				h := newHarness(t)
				h.ed.SetCaret(tt.caret, tt.anchor)
				changed := h.press(tt.key, key.ModCtrl)
				assert.Equal(t, tt.wantChanged, changed)
				assert.Equal(t, tt.want, h.ed.Text())
			},
		)
	}
}

func TestUpdateDeletionCanBeUndone(t *testing.T) {
	h := newHarness(t)
	h.ed.SetCaret(14, 14)
	h.press(key.NameDeleteBackward, key.ModCtrl)
	require.Equal(t, "buy milk ", h.ed.Text())

	update := func(gtx layout.Context) {
		for {
			if _, ok := h.ed.Update(gtx); !ok {
				break
			}
		}
	}
	h.frame(update)
	h.router.Queue(key.Event{Name: "Z", Modifiers: key.ModShortcut, State: key.Press})
	h.frame(update)
	assert.Equal(t, "buy milk today", h.ed.Text())
}

func TestUpdateReadOnly(t *testing.T) {
	t.Run(
		"deletion is ignored",
		func(t *testing.T) {
			for _, name := range []key.Name{key.NameDeleteBackward, key.NameDeleteForward} {
				h := newHarness(t)
				h.ed.ReadOnly = true
				h.ed.SetCaret(8, 8)
				assert.False(t, h.press(name, key.ModCtrl))
				assert.Equal(t, "buy milk today", h.ed.Text())
			}
		},
	)
	t.Run(
		"motion still works",
		func(t *testing.T) {
			h := newHarness(t)
			h.ed.ReadOnly = true
			h.ed.SetCaret(8, 8)
			h.press(key.NameLeftArrow, key.ModCtrl|key.ModShift)
			assert.Equal(t, "milk", h.ed.SelectedText())
		},
	)
}

func TestUpdateIgnoresUnfocusedEditor(t *testing.T) {
	h := newHarness(t)
	other := new(widget.Editor)
	h.ed.SetCaret(14, 14)
	h.router.Queue(key.Event{Name: key.NameDeleteBackward, Modifiers: key.ModCtrl, State: key.Press})
	h.frame(
		func(gtx layout.Context) {
			assert.False(t, Update(gtx, other))
			assert.True(t, Update(gtx, h.ed))
		},
	)
	assert.Equal(t, "buy milk ", h.ed.Text())
}

// harness drives a focused editor holding "buy milk today" through Gio's input
// router, so tests send it real key events.
type harness struct {
	router *input.Router
	ed     *widget.Editor
}

func newHarness(t *testing.T) *harness {
	h := &harness{router: new(input.Router), ed: new(widget.Editor)}
	h.ed.SetText("buy milk today")
	h.frame(
		func(gtx layout.Context) {
			gtx.Execute(key.FocusCmd{Tag: h.ed})
			Update(gtx, h.ed)
		},
	)
	// Focus takes effect in the next frame, which also registers the filters
	// that queued key events are matched against.
	h.frame(
		func(gtx layout.Context) {
			require.True(t, gtx.Focused(h.ed))
			Update(gtx, h.ed)
		},
	)
	return h
}

func (h *harness) press(name key.Name, mods key.Modifiers) bool {
	h.router.Queue(key.Event{Name: name, Modifiers: mods, State: key.Press})
	var changed bool
	h.frame(
		func(gtx layout.Context) {
			changed = Update(gtx, h.ed)
		},
	)
	return changed
}

func (h *harness) frame(f func(gtx layout.Context)) {
	ops := new(op.Ops)
	gtx := layout.Context{
		Ops:         ops,
		Source:      h.router.Source(),
		Constraints: layout.Exact(image.Pt(400, 100)),
	}
	f(gtx)
	// The editor's Layout would declare it focusable; the tests don't lay it out.
	for {
		if _, ok := gtx.Event(key.FocusFilter{Target: h.ed}); !ok {
			break
		}
	}
	event.Op(ops, h.ed)
	h.router.Frame(ops)
}
