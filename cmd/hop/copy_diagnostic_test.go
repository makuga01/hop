package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Explicit opt-in for an authorized source. Only the unique diagnostic folder
// is written or removed. Log counts/timings, never paths or file contents.
func TestLiveCopyDiagnostic(t *testing.T) {
	host, source, dest := os.Getenv("HOP_DIAG_HOST"), os.Getenv("HOP_DIAG_SOURCE"), os.Getenv("HOP_DIAG_DEST")
	if host == "" || source == "" || dest == "" {
		t.Skip("set HOP_DIAG_HOST, HOP_DIAG_SOURCE, HOP_DIAG_DEST")
	}
	if !validTarget(host) {
		t.Fatal("invalid host")
	}
	local, e := os.MkdirTemp(dest, ".hop-diagnostic-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(local)
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	h := Host{Target: host, Options: []string{"-o", "BatchMode=yes"}}
	s, e := connectSFTP(h)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	home, e := s.Realpath(".")
	if e != nil {
		t.Fatal(e)
	}
	source = expandRemote(source, home)
	replace := false
	if seed := os.Getenv("HOP_DIAG_SEED"); seed != "" {
		replace = true
		e = filepath.WalkDir(seed, func(name string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(seed, name)
			if err != nil {
				return err
			}
			to := filepath.Join(local, filepath.Base(source), rel)
			if entry.IsDir() {
				return os.MkdirAll(to, 0700)
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			in, err := os.Open(name)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
			if err != nil {
				return err
			}
			_, err = io.Copy(out, in)
			closeErr := out.Close()
			if err != nil {
				return err
			}
			return closeErr
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	scan := newScanProgress()
	start := time.Now()
	plan, e := planBatchObserved(s, []FileItem{{Path: source}}, local, true, replace, scan)
	if e != nil {
		t.Fatal(e)
	}
	if os.Getenv("HOP_DIAG_EXISTING_ONLY") == "1" {
		files := []PlannedFile{}
		plan.Total = 0
		for _, f := range plan.Files {
			if f.Replace {
				files = append(files, f)
				plan.Total += f.Size
			}
		}
		plan.Files = files
	}
	t.Logf("PLAN files=%d dirs=%d bytes=%d seconds=%.3f", len(plan.Files), len(plan.Directories), plan.Total, time.Since(start).Seconds())
	p := &batchProgress{embedded: true, total: plan.Total, files: len(plan.Files), started: time.Now()}
	done := make(chan error, 1)
	go func() { done <- executeBatchPlan(s, h, plan, true, Options{Yes: true, Overwrite: replace}, p) }()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	start = time.Now()
	for {
		select {
		case e := <-done:
			if e != nil {
				t.Fatal(e)
			}
			t.Logf("COPY seconds=%.3f", time.Since(start).Seconds())
			return
		case <-ticker.C:
			p.mu.Lock()
			t.Logf("PROGRESS seconds=%.1f phase=%s backend=%s files=%d bytes=%d", time.Since(start).Seconds(), p.phase, p.backend, p.doneFiles, p.completed+p.current)
			p.mu.Unlock()
		}
	}
}
