package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewFolderNamesAndSFTP(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../outside", "a/b", "/absolute", "bad\x00name", "line\nbreak"} {
		if _, e := newFolderPath("/home/user", name); e == nil {
			t.Fatal("invalid name accepted", name)
		}
	}
	s := localSFTP(t)
	parent := t.TempDir()
	for _, name := range []string{"space folder", "日本語", "literal;$(name)"} {
		target, e := newFolderPath(parent, name)
		if e != nil {
			t.Fatal(e)
		}
		if target != filepath.Join(parent, name) {
			t.Fatal("wrong creation path", target)
		}
		if e = s.Mkdir(target, 0755); e != nil {
			t.Fatal(e)
		}
		if a, e := os.Stat(target); e != nil || !a.IsDir() {
			t.Fatal("folder not created", a, e)
		}
		if e = s.Mkdir(target, 0755); e == nil {
			t.Fatal("existing folder did not error")
		}
	}
	if os.Geteuid() != 0 {
		denied := filepath.Join(parent, "denied")
		os.Mkdir(denied, 0000)
		defer os.Chmod(denied, 0700)
		if e := s.Mkdir(filepath.Join(denied, "child"), 0755); e == nil {
			t.Fatal("permission-denied create succeeded")
		}
	}
	if key, n := browserKey([]byte{14}); key != "newfolder" || n != 1 {
		t.Fatal("Ctrl-N key missing")
	}
}
