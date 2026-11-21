// Package wheel turns Gio's mouse wheel scroll distances into whole notches,
// so a notch can scroll by something meaningful, such as list items, rather
// than the few pixels Gio's X11 backend reports for one.
package wheel

import (
	"math"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/widget"
)

const (
	// Notch is the scroll distance Gio reports for one wheel notch on X11.
	Notch = 10
	// MaxNotches caps the notches a single scroll event can produce. Wayland
	// reports many times Notch for one notch, so without a cap each notch
	// would jump much further than it does on X11.
	MaxNotches = 3
)

// Accumulator collects scroll distances into whole notches. Smooth scrolling,
// as from a trackpad, reports fractions of a notch that add up across events.
// The zero value is ready to use.
type Accumulator struct {
	rest float32
}

// Add adds a scroll distance and returns the whole notches it completes, from
// -MaxNotches to MaxNotches. What's left of a notch is kept for later calls,
// unless the direction reverses or the cap is reached, when it's dropped.
func (a *Accumulator) Add(delta float32) int {
	if a.rest*delta < 0 {
		a.rest = 0
	}
	a.rest += delta
	notches := int(a.rest / Notch)
	if notches > MaxNotches || notches < -MaxNotches {
		a.rest = 0
		return max(-MaxNotches, min(notches, MaxNotches))
	}
	a.rest -= float32(notches) * Notch
	return notches
}

// List scrolls a widget.List by a number of items per wheel notch, in place of
// the list's own pixel-based wheel scrolling. The zero value is ready to use.
type List struct {
	acc Accumulator
}

// Layout lays out w, which should lay out list, and scrolls list by
// itemsPerNotch items for each wheel notch over it. It takes every wheel
// event over w along list's axis, but clicks, drags and hovers pass through
// to w, so list's scrollbar and items work as usual.
func (l *List) Layout(gtx layout.Context, list *widget.List, itemsPerNotch float32, w layout.Widget) layout.Dimensions {
	unbounded := pointer.ScrollRange{Min: math.MinInt32, Max: math.MaxInt32}
	filter := pointer.Filter{Target: l, Kinds: pointer.Scroll}
	if list.Axis == layout.Horizontal {
		filter.ScrollX = unbounded
	} else {
		filter.ScrollY = unbounded
	}
	for {
		ev, ok := gtx.Event(filter)
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok || e.Kind != pointer.Scroll {
			continue
		}
		delta := e.Scroll.Y
		if list.Axis == layout.Horizontal {
			delta = e.Scroll.X
		}
		if notches := l.acc.Add(delta); notches != 0 {
			list.ScrollBy(float32(notches) * itemsPerNotch)
		}
	}

	dims := w(gtx)
	// The area goes over w so it sees wheel events before list's own scroll
	// handling does.
	defer clip.Rect{Max: dims.Size}.Push(gtx.Ops).Pop()
	defer pointer.PassOp{}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, l)
	return dims
}
