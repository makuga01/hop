package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestLinkedSources(t *testing.T) {
	s := localSFTP(t)
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	root := t.TempDir()
	target := filepath.Join(root, "target")
	os.Mkdir(target, 0755)
	os.WriteFile(filepath.Join(target, "inside"), []byte("contents"), 0644)
	os.Symlink("target", filepath.Join(root, "linked-folder"))
	os.Symlink("target/inside", filepath.Join(root, "linked-file"))
	for _, loader := range []FileLoader{localFileLoader, remoteFileLoader(s)} {
		items, e := loader(root)
		if e != nil {
			t.Fatal(e)
		}
		found := 0
		for _, f := range items {
			if f.Name == "linked-folder" {
				found++
				if !f.Dir {
					t.Fatal("linked folder not navigable")
				}
				children, e := loader(f.Path)
				if e != nil || len(children) != 2 {
					t.Fatal(children, e)
				}
			}
			if f.Name == "linked-file" {
				found++
				if !f.Regular {
					t.Fatal("linked file not selectable")
				}
			}
		}
		if found != 2 {
			t.Fatal(items)
		}
	}
	for _, get := range []bool{false, true} {
		dest := t.TempDir()
		files, e := localSources([]string{filepath.Join(root, "linked-folder"), filepath.Join(root, "linked-file")})
		if e != nil {
			t.Fatal(e)
		}
		plan, e := planBatch(s, files, dest, get, false)
		if e != nil {
			t.Fatal(e)
		}
		p := &batchProgress{embedded: true, total: plan.Total, files: len(plan.Files), started: time.Now()}
		if e = executeBatchPlan(s, Host{Target: "link-test"}, plan, get, Options{}, p); e != nil {
			t.Fatal(e)
		}
		for _, name := range []string{"linked-folder/inside", "linked-file"} {
			b, e := os.ReadFile(filepath.Join(dest, name))
			if e != nil || string(b) != "contents" {
				t.Fatal(name, string(b), e)
			}
		}
		info, e := os.Lstat(filepath.Join(dest, "linked-folder"))
		if e != nil || !info.IsDir() {
			t.Fatal("target not materialized", e)
		}
	}
}
func TestTransferDockAndThreshold(t *testing.T) {
	if longCopy(CopyPlan{Total: 256*1024*1024 - 1}) || !longCopy(CopyPlan{Total: 256 * 1024 * 1024}) || !longCopy(CopyPlan{Files: make([]PlannedFile, 200)}) {
		t.Fatal("threshold")
	}
	strip := regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)
	for _, stage := range []string{"scan", "confirm", "copy", "done", "error"} {
		for _, size := range [][2]int{{64, 18}, {80, 24}, {110, 28}, {200, 40}} {
			d := managerFixture()
			d.transfer = &panelTransfer{stage: stage, destination: "/remote", message: "example", progress: &batchProgress{embedded: true, total: 100, completed: 50, files: 2, doneFiles: 1, phase: "Uploading", lines: []string{" ✓ first.txt", " → second.txt"}, started: time.Now()}}
			output := strip.ReplaceAllString(d.render(size[0], size[1]), "")
			if !strings.Contains(output, "/local") || !strings.Contains(output, "/remote") {
				t.Fatal("pane missing", stage)
			}
			lines := strings.Split(output, "\r\n")
			if len(lines) > size[1] {
				t.Fatal("height", stage, size, len(lines))
			}
			for _, line := range lines {
				if textWidth(line) > size[0]-1 {
					t.Fatal("width", stage, size, line)
				}
			}
			if stage == "copy" && !strings.Contains(output, "50%") {
				t.Fatal("missing global progress")
			}
		}
	}
	p := Picker{Title: "Recent local folders"}
	if strings.Contains(p.caption(), "SSH") {
		t.Fatal("wrong picker caption")
	}
}

func TestManagerDockInterrupt(t *testing.T) {
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("python3 required")
	}
	out, e := exec.Command(python, "testdata/manager_interrupt.py", os.Args[0]).CombinedOutput()
	if e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
}
func TestManagerDockInterruptHelper(t *testing.T) {
	if os.Getenv("MANAGER_DOCK_INTERRUPT") != "1" {
		t.Skip("PTY helper")
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	d := managerFixture()
	d.sftp = &SFTP{in: writer, out: reader}
	for i := range d.loaders {
		items := d.panes[i].items
		d.loaders[i] = func(string) ([]FileItem, error) { return items, nil }
	}
	screen := beginFullscreen()
	defer screen.Close()
	a, e := d.Run()
	if e != nil || a.kind != "quit" {
		t.Fatal(a, e)
	}
	fmt.Println("DOCK_INTERRUPTED")
}
