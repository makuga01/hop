package main

import (
	"testing"
	"time"
)

func TestManagerMouseFocus(t *testing.T) {
	for _, active := range []int{0, 1} {
		d := managerFixture()
		d.active = active
		other := 1 - active
		l := d.layout(110, 28)
		x := []int{l.left + 5, l.right + 5}[other]
		for _, e := range []mouseEvent{{button: 35, x: x, y: 10}, {button: 32, x: x, y: 10}, {button: 0, x: x, y: 10, release: true}, {button: 0, x: x, y: 28}} {
			d.mouse(e, 110, 28)
			if d.active != active {
				t.Fatal("non-panel-click changed focus", e)
			}
		}
		d.mouse(mouseEvent{button: 65, x: x, y: 10}, 110, 28)
		if d.active != active || d.panes[active].cursor != 1 || d.panes[other].cursor != 0 {
			t.Fatal("wheel followed pointer")
		}
		d.mouse(mouseEvent{button: 64, x: 1, y: 1}, 110, 28)
		if d.panes[active].cursor != 0 {
			t.Fatal("wheel over header did not move active cursor")
		}
		// A click on a folder in the other pane must not open it or change either selection.
		action := d.mouse(mouseEvent{button: 0, x: x, y: 9}, 110, 28)
		if action != "" || d.active != other || d.panes[other].cursor != 0 || len(d.panes[other].marks) != 0 {
			t.Fatal("focus click activated item", action)
		}
		action = d.mouse(mouseEvent{button: 0, x: x, y: 9}, 110, 28)
		if action != "" {
			t.Fatal("single click opened folder", action)
		}
		action = d.mouse(mouseEvent{button: 0, x: x, y: 9}, 110, 28)
		if action != "open:"+d.panes[other].items[0].Path {
			t.Fatal("active-pane click stopped working", action)
		}
	}
}
func TestManagerWheelKeepsSelectionBox(t *testing.T) {
	d := managerFixture()
	d.toggleFocused()
	l := d.layout(110, 28)
	d.panes[0].selectionOffset = 0
	d.mouse(mouseEvent{button: 65, x: l.right, y: l.selectionTop}, 110, 28)
	if d.active != 0 || d.panes[0].cursor != 1 || d.panes[0].selectionOffset != 0 || len(d.panes[0].marks) != 1 {
		t.Fatal("wheel over selection box changed target or marks")
	}
}

func TestManagerSingleClickOnlyPoints(t *testing.T) {
	d := managerFixture()
	l := d.layout(110, 28)
	x := l.left + 5
	if action := d.mouse(mouseEvent{button: 0, x: x, y: 10}, 110, 28); action != "" || d.panes[0].cursor != 1 || len(d.panes[0].marks) != 0 {
		t.Fatal("single click selected file", action)
	}
	d.mouse(mouseEvent{button: 0, x: x, y: 9}, 110, 28)
	d.clickAt = time.Now().Add(-time.Second)
	if action := d.mouse(mouseEvent{button: 0, x: x, y: 9}, 110, 28); action != "" {
		t.Fatal("slow clicks opened folder")
	}
	d.mouse(mouseEvent{button: 65, x: l.right, y: 10}, 110, 28)
	if action := d.mouse(mouseEvent{button: 0, x: x, y: 9}, 110, 28); action != "" {
		t.Fatal("wheel did not break double click")
	}
	d.mouse(mouseEvent{button: 2, x: x, y: 10}, 110, 28)
	if len(d.panes[0].marks) != 1 {
		t.Fatal("right click did not mark")
	}
}
