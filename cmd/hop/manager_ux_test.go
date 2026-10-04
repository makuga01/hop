package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestListingCacheTracksViewAndRefresh(t *testing.T) {
	m := newBrowserModel(Browser{Current: "/files"})
	m.items = []FileItem{{Name: "z", Path: "/files/z", Regular: true}, {Name: "a", Path: "/files/a", Regular: true}, {Name: ".hidden", Path: "/files/.hidden", Regular: true}}
	m.hideDotfiles = true
	if got := m.visible(); len(got) != 2 || got[0].Name != "a" {
		t.Fatal(got)
	}
	if allocs := testing.AllocsPerRun(100, func() { m.visible() }); allocs != 0 {
		t.Fatal("redraw still allocates", allocs)
	}
	m.query = "z"
	if got := m.visible(); len(got) != 1 || got[0].Name != "z" {
		t.Fatal(got)
	}
	m.query = ""
	m.hideDotfiles = false
	m.sortOrder.Desc = true
	if got := m.visible(); len(got) != 3 || got[0].Name != "z" {
		t.Fatal(got)
	}
	m.toggle(m.items[0])
	m.toggle(FileItem{Path: "/other/keep", Regular: true})
	m.apply(browserListing{Target: "/files", Items: []FileItem{{Name: "new", Path: "/files/new", Regular: true}}})
	if got := m.visible(); len(got) != 1 || got[0].Name != "new" {
		t.Fatal(got)
	}
	if got := m.selected(); len(got) != 1 || got[0].Path != "/other/keep" {
		t.Fatal("stale or unrelated marks", got)
	}
	m.apply(browserListing{Target: "/other", Err: errors.New("offline")})
	if len(m.selected()) != 1 {
		t.Fatal("failed listing cleared marks")
	}
}

func BenchmarkListingRedraw(b *testing.B) {
	m := newBrowserModel(Browser{})
	for i := 0; i < 24000; i++ {
		m.items = append(m.items, FileItem{Name: fmt.Sprintf("file%05d", 24000-i), Regular: true})
	}
	m.visible()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		m.visible()
	}
}
func TestToolbarTargetsMatchRenderedText(t *testing.T) {
	strip := regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	for _, w := range []int{64, 80, 96, 97, 110} {
		for _, side := range []int{0, 1} {
			d := managerFixture()
			d.active = side
			rows := strings.Split(strip.ReplaceAllString(d.render(w, 28), ""), "\r\n")[1:3]
			labels := []string{"b Machine", "c ", "n New folder", "R Refresh", ". Hidden", "o Options"}
			actions := []string{"machine", "copy", "newfolder", "refresh", "hidden", "settings"}
			for i, label := range labels {
				found := false
				for row, line := range rows {
					at := strings.Index(line, label)
					if at < 0 {
						continue
					}
					found = true
					for offset := 0; offset < textWidth(label); offset++ {
						x := textWidth(line[:at]) + 1 + offset
						if got := d.mouse(mouseEvent{button: 0, x: x, y: row + 2}, w, 28); got != actions[i] {
							t.Fatalf("%s returned %s at %d,%d", label, got, x, row+2)
						}
					}
				}
				if !found {
					t.Fatalf("missing shortcut %q at width %d: %v", label, w, rows)
				}
			}
			for row, line := range rows {
				if textWidth(line) > w-1 {
					t.Fatal("toolbar overflow", w, line)
				}
				runes := []rune(line)
				for x, r := range runes {
					if r == ' ' && x > 0 && runes[x-1] == ']' {
						if got := d.mouse(mouseEvent{button: 0, x: x + 1, y: row + 2}, w, 28); got != "" {
							t.Fatal("gap activated", got)
						}
					}
				}
			}
		}
	}
}
func TestModifiedMouseAndHiddenSelection(t *testing.T) {
	d := managerFixture()
	e, ok := parseMouse("mouse:81;10;10M") // Ctrl + wheel down.
	if !ok {
		t.Fatal("parse")
	}
	d.mouse(e, 110, 28)
	if d.panes[0].cursor != 1 {
		t.Fatal("modified wheel ignored")
	}
	d.toggleFocused()
	d.panes[0].query = "bundle"
	if !strings.Contains(d.render(110, 28), "1 outside view") {
		t.Fatal("hidden selection not disclosed")
	}
	d.panes[0].cursor = -1
	clear(d.panes[0].marks)
	if len(d.selectedAction().files) != 0 {
		t.Fatal("negative cursor selected file")
	}
}
func TestErrorDetailsAndPartialOperationRecovery(t *testing.T) {
	d := managerFixture()
	d.transfer = &panelTransfer{stage: "error", wrote: true, action: managerAction{kind: "move"}, message: strings.Repeat("very-long-path/", 50) + "permission denied"}
	if !d.transfer.modal() || strings.Contains(strings.Join(d.transfer.buttons(), ","), "Retry") {
		t.Fatal("unsafe retry / nonmodal error")
	}
	if !d.transferInput("enter", nil) || d.details == "" {
		t.Fatal("missing detail action")
	}
	for _, size := range [][2]int{{64, 18}, {110, 28}} {
		d.detailsKey("end", size[1])
		out := d.render(size[0], size[1])
		if !strings.Contains(out, "denied") {
			t.Fatal("error tail inaccessible", out)
		}
	}
	d.detailsKey("esc", 28)
	d.transferInput("esc", nil)
	if d.notice != "Move stopped · selection kept" {
		t.Fatal(d.notice)
	}
}
func TestStopKeepsSelectionAndCompletedFiles(t *testing.T) {
	d := managerFixture()
	s := localSFTP(t)
	d.sftp = s
	d.toggleFocused()
	ctx, cancel := context.WithCancel(context.Background())
	d.transfer = &panelTransfer{stage: "copy", wrote: true, ctx: ctx, cancel: cancel, action: managerAction{kind: "copy"}}
	if !d.transferInput("esc", nil) || !d.transfer.stopping || ctx.Err() == nil || s.externalContext().Err() == nil {
		t.Fatal("operation not canceled")
	}
	d.copyResult(transferResult{err: context.Canceled}, nil)
	if d.transfer.busy() || len(d.panes[0].marks) != 1 || !strings.Contains(d.transfer.message, "completed changes are kept") {
		t.Fatal("incorrect stop outcome", d.transfer)
	}
}
func TestRemovalProgressResetsCopyCounters(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "remove")
	if err := os.WriteFile(name, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := planRemoval(context.Background(), nil, []FileItem{{Path: name}}, false)
	if err != nil {
		t.Fatal(err)
	}
	p := &batchProgress{embedded: true, total: 999, completed: 999, doneFiles: 99, finished: true, lastChange: time.Now().Add(-time.Hour)}
	if err = executeRemoval(context.Background(), nil, entries, false, p); err != nil {
		t.Fatal(err)
	}
	if !p.removing || p.checking || p.checked != 1 || p.doneFiles != 1 || p.files != 1 || p.total != 0 || time.Since(p.lastChange) > time.Second {
		t.Fatalf("stale removal progress: %+v", p)
	}
}

func TestPasteCannotExecuteCommands(t *testing.T) {
	raw := pasteStart + "D\rdd\x03c" + pasteEnd
	for split := 0; split <= len(raw); split++ {
		var paste terminalPaste
		var pending []byte
		var result string
		for _, chunk := range []string{raw[:split], raw[split:]} {
			pending = append(pending, chunk...)
			for len(pending) > 0 {
				key, handled := paste.read(&pending)
				if handled {
					if key == "" {
						break
					}
					result = key
					continue
				}
				key, n := managerKey(pending)
				if n == 0 {
					break
				}
				t.Fatalf("paste leaked command %q at split %d", key, split)
			}
		}
		if result != "paste:D ddc" {
			t.Fatal(split, result)
		}
	}
	d := managerFixture()
	d.pasteText("D")
	if d.transfer != nil || !strings.Contains(d.notice, "Paste into a field") {
		t.Fatal("paste executed command")
	}
	d.panes[0].editing = true
	d.pasteText("/folder with spaces")
	if d.panes[0].edit != "/folder with spaces" {
		t.Fatal("path paste failed")
	}
	d.transfer = &panelTransfer{stage: "confirm"}
	d.pasteText("D")
	if d.transfer.stage != "confirm" || d.panes[0].edit != "/folder with spaces" {
		t.Fatal("paste affected modal dialog")
	}
	var paste terminalPaste
	pending := []byte(pasteStart + strings.Repeat("x", maxPaste+1) + pasteEnd)
	if key, ok := paste.read(&pending); !ok || key != "paste-too-large" {
		t.Fatal("silently truncated paste", key)
	}
}
