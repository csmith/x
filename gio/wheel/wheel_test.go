package wheel

import (
	"image"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"
	"github.com/stretchr/testify/assert"
)

func TestAccumulatorAdd(t *testing.T) {
	tests := []struct {
		name   string
		deltas []float32
		want   []int
	}{
		{"X11 notch down", []float32{10}, []int{1}},
		{"X11 notch up", []float32{-10}, []int{-1}},
		{"X11 notches one at a time", []float32{10, 10, -10}, []int{1, 1, -1}},
		{"X11 notches in one event", []float32{20}, []int{2}},
		{"Wayland notch is capped", []float32{150}, []int{MaxNotches}},
		{"Wayland notch up is capped", []float32{-150}, []int{-MaxNotches}},
		{"capping drops the remainder", []float32{155, 5}, []int{MaxNotches, 0}},
		{"trackpad fractions add up", []float32{2.5, 2.5, 2.5, 2.5}, []int{0, 0, 0, 1}},
		{"trackpad remainder carries over", []float32{7, 7, 7}, []int{0, 1, 1}},
		{"trackpad upwards", []float32{-6, -6}, []int{0, -1}},
		{"reversing drops the remainder", []float32{7, -4, -4}, []int{0, 0, 0}},
		{"zero keeps the remainder", []float32{7, 0, 3}, []int{0, 0, 1}},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				var acc Accumulator
				got := make([]int, len(tt.deltas))
				for i, d := range tt.deltas {
					got[i] = acc.Add(d)
				}
				assert.Equal(t, tt.want, got)
			},
		)
	}
}

func TestListScrollsByItems(t *testing.T) {
	tests := []struct {
		name          string
		axis          layout.Axis
		scroll        f32.Point
		itemsPerNotch float32
		want          int
	}{
		{"one notch", layout.Vertical, f32.Pt(0, Notch), 3, 3},
		{"two notches", layout.Vertical, f32.Pt(0, 2*Notch), 3, 6},
		{"part of a notch", layout.Vertical, f32.Pt(0, Notch/2), 3, 0},
		{"horizontal list", layout.Horizontal, f32.Pt(Notch, 0), 2, 2},
		{"horizontal wheel on vertical list", layout.Vertical, f32.Pt(Notch, 0), 3, 0},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				h := newListHarness(tt.axis, tt.itemsPerNotch)
				h.frame()
				h.router.Queue(
					pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(5, 5)},
					pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: f32.Pt(5, 5), Scroll: tt.scroll},
				)
				h.frame()
				assert.Equal(t, tt.want, h.list.Position.First)
				assert.Zero(t, h.list.Position.Offset)
			},
		)
	}
}

func TestListPassesClicksThrough(t *testing.T) {
	h := newListHarness(layout.Vertical, 1)
	h.frame()
	h.router.Queue(
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(5, 25)},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(5, 25)},
	)
	h.frame()
	assert.Equal(t, 2, h.clicked)
}

// listHarness lays out a scrolling column of buttons, 10px each and five in
// view at a time, so tests can send it real pointer events.
type listHarness struct {
	router        *input.Router
	wheel         List
	list          widget.List
	items         [20]widget.Clickable
	itemsPerNotch float32
	clicked       int
}

func newListHarness(axis layout.Axis, itemsPerNotch float32) *listHarness {
	h := &listHarness{router: new(input.Router), itemsPerNotch: itemsPerNotch, clicked: -1}
	h.list.Axis = axis
	return h
}

func (h *listHarness) frame() {
	ops := new(op.Ops)
	gtx := layout.Context{
		Ops:         ops,
		Source:      h.router.Source(),
		Constraints: layout.Exact(image.Pt(50, 50)),
	}
	h.wheel.Layout(
		gtx,
		&h.list,
		h.itemsPerNotch,
		func(gtx layout.Context) layout.Dimensions {
			return h.list.Layout(
				gtx,
				len(h.items),
				func(gtx layout.Context, i int) layout.Dimensions {
					if h.items[i].Clicked(gtx) {
						h.clicked = i
					}
					return h.items[i].Layout(
						gtx,
						func(gtx layout.Context) layout.Dimensions {
							return layout.Dimensions{Size: image.Pt(10, 10)}
						},
					)
				},
			)
		},
	)
	h.router.Frame(ops)
}
