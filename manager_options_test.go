package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConflictChoiceBothDirections(t *testing.T) {
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	s := localSFTP(t)
	for _, kind := range []string{"copy", "move"} {
		for _, side := range []int{0, 1} {
			d := newManager(t.TempDir(), t.TempDir(), Host{Target: "conflict-test"}, s, SortOrder{})
			src := filepath.Join(d.panes[side].current, "file")
			dst := filepath.Join(d.panes[1-side].current, "file")
			os.WriteFile(src, []byte("replacement"), 0644)
			os.WriteFile(dst, []byte("keep until approved"), 0644)
			f := FileItem{Path: src, Name: "file", Regular: true}
			d.panes[side].toggle(f)
			results := make(chan transferResult, 1)
			a := managerAction{kind: kind, side: side, files: []FileItem{f}}
			d.beginCopy(a, results)
			d.copyResult(<-results, results)
			if d.transfer.stage != "conflict" || strings.Contains(d.transfer.message, "--overwrite") {
				t.Fatal("missing GUI recovery", d.transfer)
			}
			if b, _ := os.ReadFile(dst); string(b) != "keep until approved" {
				t.Fatal("overwrote before approval")
			}
			d.transferInput("esc", results)
			if d.transfer != nil || len(d.panes[side].marks) != 1 {
				t.Fatal("cancel lost selection")
			}
			d.beginCopy(a, results)
			d.copyResult(<-results, results)
			d.transferInput("enter", results)
			d.copyResult(<-results, results)
			if kind == "move" {
				if d.transfer.stage != "confirm" {
					t.Fatal(d.transfer.message)
				}
				d.executeCopy(results)
			}
			d.copyResult(<-results, results)
			if d.transfer.stage != "done" {
				t.Fatal(d.transfer.message)
			}
			if b, e := os.ReadFile(dst); e != nil || string(b) != "replacement" {
				t.Fatal("replace failed", e)
			}
			_, e := os.Stat(src)
			if kind == "move" && !os.IsNotExist(e) {
				t.Fatal("move left source")
			}
			if kind == "copy" && e != nil {
				t.Fatal("copy removed source")
			}
			if d.options.Overwrite {
				t.Fatal("one-time choice changed session policy")
			}
		}
	}
}
func TestSessionOptions(t *testing.T) {
	theme := activeTheme
	defer applyTheme(theme)
	d := managerFixture()
	d.openSettings()
	for _, i := range []int{0, 1, 2, 3, 4} {
		d.settingsCursor = i
		d.settingsKey("enter", 110, 28)
	}
	if !d.options.Overwrite || !d.options.DryRun || !d.readonly || !d.options.Yes || !d.options.NoHistory || activeTheme == theme {
		t.Fatal("session options did not apply")
	}
	d.settingsCursor = 8
	d.settingsKey("enter", 110, 28)
	d.settingsText = "70000"
	d.settingsKey("enter", 110, 28)
	if !d.settingsEdit || d.settingsError == "" {
		t.Fatal("accepted invalid port")
	}
	d.settingsText = "2222"
	d.settingsKey("enter", 110, 28)
	d.connection[0] = "user@example.com"
	d.connection[2] = "/tmp/key with spaces"
	d.settingsCursor = 12
	a, done := d.settingsKey("enter", 110, 28)
	if !done || a.kind != "connect" || a.host.Target != "user@example.com" {
		t.Fatal(a)
	}
	if strings.Join(a.host.Options, "|") != "-p|2222|-i|/tmp/key with spaces" {
		t.Fatal(a.host.Options)
	}
	d.settings = true
	for _, size := range [][2]int{{64, 18}, {110, 28}} {
		if !strings.Contains(d.render(size[0], size[1]), "Options") {
			t.Fatal("options missing")
		}
	}
}
