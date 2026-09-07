package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectoryMarking(t *testing.T) {
	m := newBrowserModel(Browser{Current: "/tmp"})
	dir := FileItem{Name: "bundle", Path: "/tmp/bundle", Dir: true}
	m.toggle(FileItem{Name: "..", Path: "/", Dir: true})
	m.toggle(dir)
	if len(m.selected()) != 1 || !m.beginReview() || !m.selected()[0].Dir {
		t.Fatal("directory selection failed", m.selected())
	}
	m.toggle(dir)
	if len(m.selected()) != 0 {
		t.Fatal("directory unmark failed")
	}
}

func TestRecursiveRoundTrip(t *testing.T) {
	s := localSFTP(t)
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	root := t.TempDir()
	source := filepath.Join(root, "bundle")
	for _, p := range []string{source, filepath.Join(source, "nested"), filepath.Join(source, "empty")} {
		if e := os.Mkdir(p, 0750); e != nil {
			t.Fatal(e)
		}
	}
	contents := map[string]string{".hidden": "hidden", "nested/space file.txt": "nested contents", "zero": ""}
	for name, body := range contents {
		if e := os.WriteFile(filepath.Join(source, name), []byte(body), 0640); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.Chmod(filepath.Join(source, "nested"), 0550); e != nil {
		t.Fatal(e)
	}
	defer os.Chmod(filepath.Join(source, "nested"), 0750)
	upload, download := filepath.Join(root, "upload"), filepath.Join(root, "download")
	for _, p := range []string{upload, download} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	files, e := localSources([]string{source, filepath.Join(source, "nested/space file.txt")})
	if e != nil || len(files) != 2 || !files[0].Dir {
		t.Fatal("explicit directory source", files, e)
	}
	plan, e := planBatch(s, files, upload, false, false)
	if e != nil {
		t.Fatal(e)
	}
	if len(plan.Roots) != 1 || len(plan.Files) != 3 || len(plan.Directories) != 3 || plan.Total != uint64(len("hiddennested contents")) {
		t.Fatalf("wrong recursive plan: %+v", plan)
	}
	h := Host{Target: "recursive-test"}
	if e = copyBatch(s, h, files, upload, false, Options{DryRun: true}); e != nil {
		t.Fatal(e)
	}
	entries, _ := os.ReadDir(upload)
	if len(entries) != 0 {
		t.Fatal("dry run created directories")
	}
	if e = copyBatch(s, h, files, upload, false, Options{Yes: true}); e != nil {
		t.Fatal(e)
	}
	remote := []FileItem{{Name: "bundle", Path: filepath.Join(upload, "bundle"), Dir: true}}
	if e = copyBatch(s, h, remote, download, true, Options{Yes: true}); e != nil {
		t.Fatal(e)
	}
	for _, dest := range []string{upload, download} {
		defer os.Chmod(filepath.Join(dest, "bundle/nested"), 0750)
		for name, body := range contents {
			got, e := os.ReadFile(filepath.Join(dest, "bundle", name))
			if e != nil || string(got) != body {
				t.Fatalf("recursive contents %s: %q %v", name, got, e)
			}
		}
		for name, perm := range map[string]os.FileMode{"empty": 0750, "nested": 0550} {
			a, e := os.Stat(filepath.Join(dest, "bundle", name))
			if e != nil || !a.IsDir() || a.Mode().Perm() != perm {
				t.Fatalf("folder mode %s: %v %v", name, a, e)
			}
		}
	}
	// Existing folders merge without changing their permissions or unrelated files.
	if e = os.Chmod(filepath.Join(upload, "bundle"), 0711); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(upload, "bundle/keep"), []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	// The nested folder needs to be writable to replace its files.
	if e = os.Chmod(filepath.Join(upload, "bundle/nested"), 0750); e != nil {
		t.Fatal(e)
	}
	if e = copyBatch(s, h, files, upload, false, Options{Yes: true, Overwrite: true}); e != nil {
		t.Fatal(e)
	}
	a, _ := os.Stat(filepath.Join(upload, "bundle"))
	if a.Mode().Perm() != 0711 {
		t.Fatal("changed existing folder permissions")
	}
	if got, e := os.ReadFile(filepath.Join(upload, "bundle/keep")); e != nil || string(got) != "keep" {
		t.Fatal("lost unrelated file")
	}
}

func TestRecursivePreflightRefusesBeforeWriting(t *testing.T) {
	s := localSFTP(t)
	for _, get := range []bool{false, true} {
		for _, problem := range []string{"conflict", "symlink", "denied", "destination-link"} {
			t.Run(map[bool]string{false: "upload", true: "download"}[get]+"/"+problem, func(t *testing.T) {
				root := t.TempDir()
				source, dest := filepath.Join(root, "tree"), filepath.Join(root, "dest")
				for _, p := range []string{source, dest, filepath.Join(source, "a-empty")} {
					if e := os.Mkdir(p, 0700); e != nil {
						t.Fatal(e)
					}
				}
				if e := os.WriteFile(filepath.Join(source, "z-file"), []byte("source"), 0600); e != nil {
					t.Fatal(e)
				}
				switch problem {
				case "conflict":
					os.Mkdir(filepath.Join(dest, "tree"), 0700)
					os.WriteFile(filepath.Join(dest, "tree/z-file"), []byte("original"), 0600)
				case "symlink":
					if e := os.Symlink(source, filepath.Join(source, "z-link")); e != nil {
						t.Fatal(e)
					}
				case "denied":
					if os.Geteuid() == 0 {
						t.Skip("root bypasses permissions")
					}
					p := filepath.Join(source, "z-denied")
					os.Mkdir(p, 0000)
					defer os.Chmod(p, 0700)
				case "destination-link":
					outside := filepath.Join(root, "outside")
					os.Mkdir(outside, 0700)
					if e := os.Symlink(outside, filepath.Join(dest, "tree")); e != nil {
						t.Fatal(e)
					}
				}
				e := copyBatch(s, Host{Target: "recursive-test"}, []FileItem{{Name: "tree", Path: source, Dir: true}}, dest, get, Options{Yes: true})
				if e == nil {
					t.Fatal("accepted", problem)
				}
				if problem == "symlink" && !strings.Contains(e.Error(), "symlink") {
					t.Fatal("missing symlink explanation", e)
				}
				if _, e = os.Stat(filepath.Join(dest, "tree/a-empty")); !os.IsNotExist(e) {
					t.Fatal("created folder before preflight completed")
				}
				if problem == "conflict" {
					got, _ := os.ReadFile(filepath.Join(dest, "tree/z-file"))
					if string(got) != "original" {
						t.Fatal("overwrote conflict")
					}
				}
			})
		}
	}
}
