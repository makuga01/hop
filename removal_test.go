package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemovalBothEnds(t *testing.T) {
	s := localSFTP(t)
	for _, remote := range []bool{false, true} {
		root := t.TempDir()
		tree := filepath.Join(root, "tree")
		os.MkdirAll(filepath.Join(tree, "empty"), 0755)
		os.WriteFile(filepath.Join(tree, ".hidden"), []byte("data"), 0644)
		target := filepath.Join(root, "keep")
		os.Mkdir(target, 0755)
		os.WriteFile(filepath.Join(target, "precious"), []byte("keep"), 0644)
		os.Symlink(target, filepath.Join(tree, "link"))
		os.Symlink("missing", filepath.Join(tree, "broken"))
		ctx := context.Background()
		p, e := planRemoval(ctx, s, []FileItem{{Path: tree, Dir: true}}, remote)
		if e != nil {
			t.Fatal(e)
		}
		if e = executeRemoval(ctx, s, p, remote, nil); e != nil {
			t.Fatal(e)
		}
		if _, e = os.Lstat(tree); !os.IsNotExist(e) {
			t.Fatal("tree remains", e)
		}
		if b, e := os.ReadFile(filepath.Join(target, "precious")); e != nil || string(b) != "keep" {
			t.Fatal("followed symlink on delete", e)
		}
	}
}
func TestRemovalKeepsChangedSelection(t *testing.T) {
	s := localSFTP(t)
	for _, remote := range []bool{false, true} {
		for _, change := range []string{"new-file", "modified", "cancel", "directory-link"} {
			root := t.TempDir()
			tree := filepath.Join(root, "tree")
			os.Mkdir(tree, 0755)
			file := filepath.Join(tree, "original")
			os.WriteFile(file, []byte("original"), 0644)
			ctx, cancel := context.WithCancel(context.Background())
			p, e := planRemoval(ctx, s, []FileItem{{Path: tree, Dir: true}}, remote)
			if e != nil {
				t.Fatal(e)
			}
			switch change {
			case "new-file":
				os.WriteFile(filepath.Join(tree, "new"), []byte("keep"), 0644)
			case "modified":
				os.WriteFile(file, []byte("changed and larger"), 0644)
			case "cancel":
				cancel()
			case "directory-link":
				os.Rename(tree, tree+"-moved")
				os.Symlink(tree+"-moved", tree)
			}
			if e = executeRemoval(ctx, s, p, remote, nil); e == nil {
				t.Fatal("deleted changed/cancelled selection", remote, change)
			}
			cancel()
			if _, e = os.Stat(file); e != nil {
				t.Fatal("lost source", remote, change, e)
			}
		}
	}
}
func TestManagerMoveBothDirections(t *testing.T) {
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	s := localSFTP(t)
	for _, side := range []int{0, 1} {
		local, remote := t.TempDir(), t.TempDir()
		d := newManager(local, remote, Host{Target: "move-test"}, s, SortOrder{})
		src := filepath.Join(d.panes[side].current, "bundle")
		os.Mkdir(src, 0755)
		os.WriteFile(filepath.Join(src, "data"), []byte("move me"), 0644)
		results := make(chan transferResult, 1)
		d.beginCopy(managerAction{kind: "move", side: side, files: []FileItem{{Path: src, Dir: true}}}, results)
		d.copyResult(<-results, results)
		if d.transfer.stage != "confirm" {
			t.Fatal("move must confirm", d.transfer)
		}
		if _, e := os.Stat(src); e != nil {
			t.Fatal("source removed before confirmation")
		}
		d.executeCopy(results)
		d.copyResult(<-results, results)
		if d.transfer.stage != "done" {
			t.Fatal(d.transfer.message)
		}
		if _, e := os.Stat(src); !os.IsNotExist(e) {
			t.Fatal("move retained source", e)
		}
		b, e := os.ReadFile(filepath.Join(d.panes[1-side].current, "bundle/data"))
		if e != nil || string(b) != "move me" {
			t.Fatal("missing copied data", e)
		}
	}
}
func TestManagerModes(t *testing.T) {
	d := managerFixture()
	m := d.panes[0]
	if d.normalKey("text:/") != "filter" {
		t.Fatal("filter shortcut")
	}
	d.filtering[0] = true
	for _, r := range "hjklDd /" {
		key := "text:" + string(r)
		if r == ' ' {
			key = "space"
		}
		d.filterKey(key)
	}
	if m.query != "hjklDd /" {
		t.Fatal("filter interpreted command", m.query)
	}
	d.filterKey("esc")
	if d.filtering[0] || m.query != "hjklDd /" {
		t.Fatal("Esc lost filter")
	}
	if d.normalKey("text:d") != "" || d.normalKey("text:d") != "delete" {
		t.Fatal("dd")
	}
	d.normalKey("text:d")
	d.prefixAt = time.Now().Add(-2 * time.Second)
	if d.normalKey("text:d") == "delete" {
		t.Fatal("stale d prefix")
	}
	d.normalKey("text:g")
	if d.normalKey("text:g") != "home" {
		t.Fatal("gg")
	}
	if d.normalKey("text:h") != "help" || d.normalKey("text:H") != "left" {
		t.Fatal("help navigation conflict")
	}
	d.help = true
	for _, size := range [][2]int{{64, 18}, {110, 28}, {200, 40}} {
		out := d.render(size[0], size[1])
		if !strings.Contains(out, "Commands") {
			t.Fatal("help table missing")
		}
	}
}

func TestMoveKeepsSourcesOnFailure(t *testing.T) {
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	s := localSFTP(t)
	for _, side := range []int{0, 1} {
		d := newManager(t.TempDir(), t.TempDir(), Host{Target: "move-failure"}, s, SortOrder{})
		src := filepath.Join(d.panes[side].current, "file")
		os.WriteFile(src, []byte("original"), 0644)
		results := make(chan transferResult, 1)
		d.beginCopy(managerAction{kind: "move", side: side, files: []FileItem{{Path: src, Regular: true}}}, results)
		d.copyResult(<-results, results)
		if d.transfer.stage != "confirm" {
			t.Fatal(d.transfer.message)
		}
		// A conflicting destination appearing after review must not remove the source.
		dest := filepath.Join(d.panes[1-side].current, "file")
		os.WriteFile(dest, []byte("keep destination"), 0644)
		d.executeCopy(results)
		d.copyResult(<-results, results)
		if d.transfer.stage != "conflict" {
			t.Fatal("missing conflict choice")
		}
		if b, e := os.ReadFile(src); e != nil || string(b) != "original" {
			t.Fatal("lost source", e)
		}
		if b, e := os.ReadFile(dest); e != nil || string(b) != "keep destination" {
			t.Fatal("clobbered destination", e)
		}
	}
}
func TestDeleteDryRun(t *testing.T) {
	s := localSFTP(t)
	d := newManager(t.TempDir(), t.TempDir(), Host{}, s, SortOrder{})
	d.options.DryRun = true
	src := filepath.Join(d.panes[0].current, "file")
	os.WriteFile(src, []byte("keep"), 0644)
	results := make(chan transferResult, 1)
	d.beginCopy(managerAction{kind: "delete", files: []FileItem{{Path: src, Regular: true}}}, results)
	d.copyResult(<-results, results)
	if d.transfer.stage != "done" {
		t.Fatal(d.transfer.message)
	}
	if _, e := os.Stat(src); e != nil {
		t.Fatal("dry run removed source", e)
	}
	if _, e := planRemoval(context.Background(), s, []FileItem{{Path: "/", Dir: true}}, false); e == nil {
		t.Fatal("accepted filesystem root")
	}
}
