package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMouseParsingAndScrolling(t *testing.T) {
	for _, seq := range []string{"\x1b[<0;8;7M", "\x1b[<2;8;7m", "\x1b[<65;79;12M", "\x1b[<32;79;11M"} {
		for i := 1; i < len(seq); i++ {
			if _, n := browserKey([]byte(seq[:i])); n != 0 {
				t.Fatalf("partial mouse packet consumed at %d", i)
			}
		}
		key, n := browserKey([]byte(seq + "x"))
		e, ok := parseMouse(key)
		if n != len(seq) || !ok || e.x < 1 {
			t.Fatal("mouse decoding", key, n, e)
		}
	}
	for _, bad := range []string{"mouse:0;0;8M", "mouse:0;-1;7M", "mouse:x;2;3M", "mouse:2;8M"} {
		if _, ok := parseMouse(bad); ok {
			t.Fatal("malformed mouse accepted", bad)
		}
	}
	b := Browser{Current: "/mouse"}
	m := newBrowserModel(b)
	for i := 0; i < 60; i++ {
		m.items = append(m.items, FileItem{Name: fmt.Sprintf("file%02d", i), Path: fmt.Sprintf("/mouse/file%02d", i), Regular: true})
	}
	b.render(m, 80, 24)
	b.mouse(m, mouseEvent{button: 65, x: 10, y: 10}, 80, 24)
	b.render(m, 80, 24)
	if m.offset != 0 || m.cursor != 1 {
		t.Fatal("wheel did not move cursor within viewport", m.offset, m.cursor)
	}
	b.mouse(m, mouseEvent{button: 0, x: 10, y: 8}, 80, 24)
	if len(m.marks) != 1 || m.selected()[0].Path != "/mouse/file00" || m.review {
		t.Fatal("left click did not mark visible file")
	}
	b.mouse(m, mouseEvent{button: 0, x: 10, y: 8, release: true}, 80, 24)
	if len(m.marks) != 1 {
		t.Fatal("release toggled file")
	}
	b.render(m, 80, 24)
	rows, _ := browserLayout(m, 24)
	b.mouse(m, mouseEvent{button: 0, x: 77, y: 7 + rows}, 80, 24)
	b.mouse(m, mouseEvent{button: 32, x: 77, y: 8}, 80, 24)
	b.mouse(m, mouseEvent{button: 0, x: 77, y: 8, release: true}, 80, 24)
	if m.offset != 0 || m.dragging != 0 {
		t.Fatal("scrollbar drag failed", m.offset, m.dragging)
	}
	if !strings.Contains(b.render(m, 80, 24), "█") {
		t.Fatal("missing scrollbar thumb")
	}
	for i := 1; i < 10; i++ {
		m.toggle(m.items[i])
	}
	b.render(m, 80, 24)
	rows, selectedRows := browserLayout(m, 24)
	tail := m.selectionStart(selectedRows)
	b.mouse(m, mouseEvent{button: 64, x: 10, y: 14 + rows}, 80, 24)
	if m.selectionStart(selectedRows) != max(0, tail-1) {
		t.Fatal("selection wheel failed")
	}
	// The selection scrollbar also supports dragging to the first mark.
	b.mouse(m, mouseEvent{button: 0, x: 77, y: 13 + rows + selectedRows}, 80, 24)
	b.mouse(m, mouseEvent{button: 32, x: 77, y: 14 + rows}, 80, 24)
	b.mouse(m, mouseEvent{button: 0, x: 77, y: 14 + rows, release: true}, 80, 24)
	if m.selectionStart(selectedRows) != 0 {
		t.Fatal("selection scrollbar drag failed")
	}
	// Reviewing and clicking does not accept the batch; right click removes one.
	m.beginReview()
	b.render(m, 80, 24)
	before := len(m.marks)
	b.mouse(m, mouseEvent{button: 0, x: 10, y: 8}, 80, 24)
	if len(m.marks) != before {
		t.Fatal("left click removed review item")
	}
	b.mouse(m, mouseEvent{button: 2, x: 10, y: 8}, 80, 24)
	if len(m.marks) != before-1 {
		t.Fatal("right click did not remove review item")
	}
}

func TestWheelMovesCursorBeforeViewport(t *testing.T) {
	for _, review := range []bool{false, true} {
		b := Browser{Current: "/mouse"}
		m := newBrowserModel(b)
		for i := 0; i < 60; i++ {
			f := FileItem{Name: fmt.Sprintf("file%02d", i), Path: fmt.Sprintf("/mouse/file%02d", i), Regular: true}
			m.items = append(m.items, f)
			if review {
				m.toggle(f)
			}
		}
		m.review = review
		rows, _ := browserLayout(m, 24)
		for _, test := range []struct{ cursor, offset, button, wantCursor, wantOffset int }{
			{3, 0, 65, 4, 0},
			{3, 0, 64, 2, 0},
			{rows - 1, 0, 65, rows, 1},
			{rows, 1, 64, rows - 1, 1},
			{1, 1, 64, 0, 0},
			{0, 0, 64, 0, 0},
			{59, 60 - rows, 65, 59, 60 - rows},
		} {
			if review {
				m.reviewCursor = test.cursor
				m.reviewOffset = test.offset
			} else {
				m.cursor = test.cursor
				m.offset = test.offset
			}
			b.mouse(m, mouseEvent{button: test.button, x: 10, y: 10}, 80, 24)
			cursor, offset := m.view(rows)
			if cursor != test.wantCursor || offset != test.wantOffset {
				t.Fatalf("review=%v, %+v: got cursor %d, offset %d", review, test, cursor, offset)
			}
		}
	}
}

func TestMouseDirectoryAndBlankClicks(t *testing.T) {
	b := Browser{Current: "/mouse"}
	m := newBrowserModel(b)
	m.items = []FileItem{{Name: "..", Path: "/", Dir: true}, {Name: "folder", Path: "/mouse/folder", Dir: true}, {Name: "file", Path: "/mouse/file", Regular: true}}
	if p := b.mouse(m, mouseEvent{button: 0, x: 8, y: 9}, 80, 24); p != "/mouse/folder" {
		t.Fatal("directory click", p)
	}
	b.mouse(m, mouseEvent{button: 2, x: 8, y: 9}, 80, 24)
	if len(m.marks) != 1 || !m.selected()[0].Dir {
		t.Fatal("right click did not mark directory")
	}
	b.mouse(m, mouseEvent{button: 2, x: 8, y: 8}, 80, 24)
	b.mouse(m, mouseEvent{button: 0, x: 8, y: 4}, 80, 24)
	b.mouse(m, mouseEvent{button: 0, x: 8, y: 14}, 80, 24)
	if len(m.marks) != 1 {
		t.Fatal("parent/header/blank click marked")
	}
	b.FolderOnly = true
	b.mouse(m, mouseEvent{button: 2, x: 8, y: 10}, 80, 24)
	if len(m.marks) != 1 {
		t.Fatal("destination picker mutated source selection")
	}
}

func TestMouseTerminal(t *testing.T) {
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("python3 required")
	}
	for _, encoding := range []string{"sgr", "legacy"} {
		t.Run(encoding, func(t *testing.T) {
			out, e := exec.Command(python, "testdata/mouse_browser.py", os.Args[0], encoding).CombinedOutput()
			if e != nil {
				t.Fatalf("mouse PTY: %v\n%s", e, out)
			}
		})
	}
}
func TestMouseTerminalHelper(t *testing.T) {
	if os.Getenv("HOP_MOUSE_TEST") != "1" {
		t.Skip("PTY helper")
	}
	root := "/mouse"
	loader := func(p string) ([]FileItem, error) {
		if p == root+"/private" {
			return nil, &StatusError{Code: 3, Message: "Permission denied"}
		}
		if p != root {
			return sortedListing(p, []FileItem{{Name: "inside.txt", Path: p + "/inside.txt", Regular: true}}), nil
		}
		items := []FileItem{{Name: "folder", Path: p + "/folder", Dir: true}, {Name: "private", Path: p + "/private", Dir: true}}
		for i := 0; i < 40; i++ {
			name := fmt.Sprintf("file%02d.txt", i)
			items = append(items, FileItem{Name: name, Path: p + "/" + name, Regular: true})
		}
		return sortedListing(p, items), nil
	}
	result, e := (Browser{Title: "MOUSE_TEST", Current: root, Home: root, Load: loader}).Run()
	if e != nil {
		t.Fatal(e)
	}
	if len(result.Files) != 3 {
		t.Fatal("mouse selections", result)
	}
	want := []string{root + "/file00.txt", root + "/folder", root + "/file39.txt"}
	for i, f := range result.Files {
		if f.Path != want[i] {
			t.Fatal("wrong mouse target", i, f)
		}
	}
	println("MOUSE_OK")
}

func TestLegacyMousePacketsRemainAtomic(t *testing.T) {
	for _, event := range []mouseEvent{{button: 0, x: 67, y: 68}, {button: 3, x: 8, y: 9, release: true}, {button: 65, x: 20, y: 10}, {button: 32, x: 40, y: 12}} {
		packet := []byte{27, '[', 'M', byte(event.button + 32), byte(event.x + 32), byte(event.y + 32)}
		for split := 1; split < len(packet); split++ {
			if _, n := managerKey(packet[:split]); n != 0 {
				t.Fatalf("partial legacy packet consumed at %d", split)
			}
		}
		key, n := managerKey(append(packet, 'q'))
		got, ok := parseMouse(key)
		if !ok || n != 6 || got != event {
			t.Fatalf("legacy mouse: %+v, %q, %d", got, key, n)
		}
		if key, n = managerKey(append(packet, 'q')[n:]); key != "text:q" || n != 1 {
			t.Fatal("following key lost")
		}
	}
}

func TestMouseDiagnosticTerminal(t *testing.T) {
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("python3 required")
	}
	output, e := exec.Command(python, "testdata/mouse_diagnostic.py", os.Args[0]).CombinedOutput()
	if e != nil {
		t.Fatalf("diagnostic PTY: %v\n%s", e, output)
	}
}
func TestMouseDiagnosticHelper(t *testing.T) {
	if os.Getenv("HOP_DIAGNOSTIC_TEST") != "1" {
		t.Skip("PTY helper")
	}
	if e := diagnoseMouse(); e != nil {
		t.Fatal(e)
	}
}
