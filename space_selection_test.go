package main

import (
	"os"
	"os/exec"
	"testing"
)

func TestSpaceSelectionTerminal(t *testing.T) {
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("python3 required")
	}
	out, e := exec.Command(python, "testdata/space_selection.py", os.Args[0]).CombinedOutput()
	if e != nil {
		t.Fatalf("Space selection PTY: %v\n%s", e, out)
	}
}
func TestSpaceSelectionHelper(t *testing.T) {
	if os.Getenv("SPACE_SELECTION_TEST") != "1" {
		t.Skip("PTY helper")
	}
	files := []FileItem{{Name: "bundle", Path: "/space/bundle", Dir: true}, {Name: "alpha.txt", Path: "/space/alpha.txt", Regular: true}, {Name: "beta.txt", Path: "/space/beta.txt", Regular: true}}
	r, e := (Browser{Title: "Space selection", Current: "/space", StartRecent: true, Recent: files}).Run()
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Files) != 1 || r.Files[0].Path != "/space/bundle" {
		t.Fatal("Space moved the pointer", r)
	}
	println("SPACE_STAYS_OK")
}
