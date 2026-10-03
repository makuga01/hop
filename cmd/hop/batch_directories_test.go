package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectoryPreparationOrderingAndSafety(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	child := filepath.Join(parent, "child")
	// Deliberately reversed input; dependency order cannot rely on input order.
	dirs := []PlannedDirectory{{Local: child, Mode: 0500}, {Local: parent, Mode: 0500}}
	status := newActivity(io.Discard, false, false, "Preparing", 0, false)
	defer status.Stop()
	created, err := prepareDirectories(nil, dirs, true, status, nil)
	if err != nil || len(created) != 2 {
		t.Fatal("preparation failed", err)
	}
	defer os.Chmod(parent, 0700)
	defer os.Chmod(child, 0700)
	if err = finishDirectories(nil, created, true, status); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{parent, child} {
		info, e := os.Stat(name)
		if e != nil || info.Mode().Perm() != 0500 {
			t.Fatal("permissions not applied", e)
		}
	}
	os.Chmod(parent, 0700)
	os.Chmod(child, 0700)
	created, err = prepareDirectories(nil, dirs, true, status, nil)
	if err != nil || len(created) != 0 {
		t.Fatal("existing dirs not reused", err)
	}
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err = os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	bad := []PlannedDirectory{{Local: filepath.Join(link, "child")}, {Local: link}}
	if _, err = prepareDirectories(nil, bad, true, status, nil); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err = os.Lstat(filepath.Join(outside, "child")); !os.IsNotExist(err) {
		t.Fatal("created through rejected symlink")
	}
}
