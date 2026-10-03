package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParallelScanTreeAndCycles(t *testing.T) {
	s := localSFTP(t)
	source := filepath.Join(t.TempDir(), "tree")
	os.Mkdir(source, 0700)
	for i := 0; i < 24; i++ {
		dir := filepath.Join(source, fmt.Sprint(i))
		os.Mkdir(dir, 0700)
		os.WriteFile(filepath.Join(dir, "file"), []byte("sample"), 0600)
	}
	scan := newScanProgress()
	plan, e := planBatchObserved(s, []FileItem{{Path: source}}, t.TempDir(), true, false, scan)
	if e != nil {
		t.Fatal(e)
	}
	if len(plan.Files) != 24 || len(plan.Directories) != 25 || plan.Total != 144 {
		t.Fatal("incomplete tree")
	}
	if scan.files != 24 || scan.dirs != 25 || scan.checked != 24 || scan.phase != "Scan complete" {
		t.Fatalf("incomplete scan progress: files=%d dirs=%d checked=%d phase=%s", scan.files, scan.dirs, scan.checked, scan.phase)
	}
	if e := os.Symlink(source, filepath.Join(source, "0", "loop")); e != nil {
		t.Fatal(e)
	}
	_, e = planBatch(s, []FileItem{{Path: source}}, t.TempDir(), true, false)
	if e == nil || !strings.Contains(e.Error(), "symlink loop") {
		t.Fatal("cycle accepted", e)
	}
}
func TestPlanRejectsRootBeforeRemoteTraversal(t *testing.T) {
	s := localSFTP(t)
	_, e := planBatch(s, []FileItem{{Path: "/"}}, t.TempDir(), true, false)
	if e == nil || !strings.Contains(e.Error(), "filesystem root") {
		t.Fatal(e)
	}
}
func TestNewUploadTreeSkipsImpossibleDestinationChecks(t *testing.T) {
	source := filepath.Join(t.TempDir(), "tree")
	os.MkdirAll(filepath.Join(source, "a", "b"), 0700)
	for _, name := range []string{"first", "a/second", "a/b/third"} {
		os.WriteFile(filepath.Join(source, name), []byte("data"), 0600)
	}
	stats := 0
	s := protocolFixture(t, func(typ byte, b []byte) (byte, []byte) {
		d := decoder{b: b}
		id := d.u32()
		name := d.str()
		switch typ {
		case fxRealpath:
			return fxName, fields(u32(id), u32(1), str("/target"), str(""), u32(0))
		case fxStat:
			return fxAttrs, fields(u32(id), u32(4), u32(0040700))
		case fxLstat:
			stats++
			if name != "/target/tree" {
				t.Errorf("unnecessary missing descendant request: %s", name)
			}
			return fxStatus, fields(u32(id), u32(fxNoSuch), str("missing"), str(""))
		default:
			t.Errorf("unexpected request %d", typ)
			return fxStatus, fields(u32(id), u32(4), str("unexpected"), str(""))
		}
	})
	p, e := planBatch(s, []FileItem{{Path: source}}, "/target", false, false)
	if e != nil {
		t.Fatal(e)
	}
	if stats != 1 || len(p.Files) != 3 || len(p.Directories) != 3 {
		t.Fatal("unexpected preflight", stats)
	}
}

// Explicitly opt in: reads remote metadata and opens files read-only to check
// access. Does not read file contents or copy anything to the local machine.
func TestLivePlanOnly(t *testing.T) {
	target, source := os.Getenv("HOP_PLAN_HOST"), os.Getenv("HOP_PLAN_SOURCE")
	if target == "" || source == "" {
		t.Skip("set HOP_PLAN_HOST and HOP_PLAN_SOURCE")
	}
	if !validTarget(target) {
		t.Fatal("invalid host")
	}
	s, e := connectSFTP(Host{Target: target, Options: []string{"-o", "BatchMode=yes"}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	home, e := s.Realpath(".")
	if e != nil {
		t.Fatal(e)
	}
	source = expandRemote(source, home)
	start := time.Now()
	scan := newScanProgress()
	p, e := planBatchObserved(s, []FileItem{{Path: source}}, t.TempDir(), true, false, scan)
	if e != nil {
		t.Fatal(e)
	}
	scan.mu.Lock()
	for phase, duration := range scan.durations {
		t.Logf("SCAN_PHASE %s seconds=%.3f", phase, duration.Seconds())
	}
	scan.mu.Unlock()
	t.Logf("PLAN_ONLY files=%d directories=%d seconds=%.3f", len(p.Files), len(p.Directories), time.Since(start).Seconds())
}

func TestParallelScanSharedDirectoryAliases(t *testing.T) {
	s := localSFTP(t)
	root := t.TempDir()
	source := filepath.Join(root, "tree")
	os.Mkdir(source, 0700)
	real := filepath.Join(source, "real")
	os.Mkdir(real, 0700)
	os.WriteFile(filepath.Join(real, "file"), []byte("same"), 0600)
	for _, name := range []string{"alias1", "alias2"} {
		if e := os.Symlink(real, filepath.Join(source, name)); e != nil {
			t.Fatal(e)
		}
	}
	p, e := planBatch(s, []FileItem{{Path: source}}, t.TempDir(), true, false)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Files) != 3 || len(p.Directories) != 4 || p.Total != 12 {
		t.Fatal("aliases incorrectly deduplicated", len(p.Files), len(p.Directories))
	}
}
func TestPlanningWorkerHandleBudget(t *testing.T) {
	for _, limit := range []uint64{1, 8, 512} {
		s := &SFTP{}
		s.limitsOnce.Do(func() { s.limits = transferLimits{negotiated: true, handles: limit} })
		got, e := s.planningWorkers()
		if e != nil || got != int(min(uint64(256), max(uint64(1), limit/2))) {
			t.Fatal(limit, got, e)
		}
	}
}
