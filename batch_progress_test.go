package main

import (
	"os"
	"strings"
	"testing"
)

func TestBatchProgressCarriesBytesAndDoesNotClear(t *testing.T) {
	f, e := os.CreateTemp(t.TempDir(), "screen")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	screen := &fullScreen{tty: f, width: 100, height: 24}
	plan := CopyPlan{Total: 10, Files: make([]PlannedFile, 2)}
	p := startBatchProgress(screen, plan, Host{Target: "test"})
	defer p.Stop()
	p.beginFile(0, PlannedFile{Remote: "/to/a", Size: 4}, false, "/to")
	child := p.activity("Uploading", 4, true)
	child.Update(4)
	child.Phase("Verifying")
	p.mu.Lock()
	percent := p.percent()
	p.mu.Unlock()
	if percent != 40 {
		t.Fatal("wrong global percentage", percent)
	}
	p.completeFile(nil)
	child.Stop()
	p.beginFile(1, PlannedFile{Remote: "/to/b", Size: 6}, false, "/to")
	child = p.activity("Uploading", 6, true)
	child.Update(3)
	p.mu.Lock()
	percent = p.percent()
	p.mu.Unlock()
	if percent != 70 {
		t.Fatal("progress reset between files", percent)
	}
	child.Update(6)
	p.completeFile(nil)
	p.mu.Lock()
	percent = p.percent()
	p.mu.Unlock()
	if percent >= 100 {
		t.Fatal("reported completion before finalization")
	}
	p.finish()
	p.Stop()
	data, e := os.ReadFile(f.Name())
	if e != nil {
		t.Fatal(e)
	}
	if strings.Count(string(data), "\x1b[2J") != 1 {
		t.Fatal("screen cleared between files")
	}
	for _, want := range []string{"100%", "2/2 files", "✓ a", "✓ b"} {
		if !strings.Contains(string(data), want) {
			t.Fatal("missing batch status", want)
		}
	}
}
