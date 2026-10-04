package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUploadScanMatchesSFTP(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3")
	}
	s := localSFTP(t)
	source := filepath.Join(t.TempDir(), "source")
	dest := t.TempDir()
	for i := 0; i < 96; i++ {
		rel := filepath.Join(fmt.Sprintf("folder-%d", i%12), fmt.Sprintf("file-%d", i))
		local := filepath.Join(source, rel)
		os.MkdirAll(filepath.Dir(local), 0700)
		os.WriteFile(local, []byte("source"), 0600)
		if i%2 == 0 {
			remote := filepath.Join(dest, "source", rel)
			os.MkdirAll(filepath.Dir(remote), 0700)
			os.WriteFile(remote, []byte("old"), 0600)
		}
	}
	roots := []FileItem{{Path: source}}
	before, err := planBatch(s, roots, dest, false, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{uploadScanPython, "raise SystemExit(1)", "import sys;sys.stdout.buffer.write(b'HPSTAT1\\n'+b'x'*1000)"} {
		s.uploadCheckCommand = func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, python, "-c", script) }
		after, err := planBatch(s, roots, dest, false, true)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatal("helper/fallback plan differs from SFTP")
		}
	}
	s.uploadCheckCommand = func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, python, "-c", uploadScanPython) }
	if _, err := planBatch(s, roots, dest, false, false); err == nil {
		t.Fatal("overwrite accepted without consent")
	}
	remote := filepath.Join(dest, "source", "folder-0", "file-0")
	os.Remove(remote)
	os.Symlink(filepath.Join(source, "folder-0", "file-0"), remote)
	if _, err := planBatch(s, roots, dest, false, true); err == nil {
		t.Fatal("destination symlink accepted")
	}
	os.Remove(remote)
	os.WriteFile(remote, []byte("old"), 0600)
	folder := filepath.Join(dest, "source", "folder-1")
	os.Symlink(filepath.Join(source, "folder-1"), folder)
	if _, err := planBatch(s, roots, dest, false, true); err == nil {
		t.Fatal("destination directory symlink accepted")
	}
}

func TestUploadScanRejectsNamespaceMismatch(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3")
	}
	s := localSFTP(t)
	script := strings.Replace(uploadScanPython, "if os.path.realpath(root)!=canonical:", "if True:", 1)
	s.uploadCheckCommand = func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, python, "-c", script) }
	plan := CopyPlan{Destination: t.TempDir(), Files: make([]PlannedFile, 32)}
	for i := range plan.Files {
		plan.Files[i].Remote = filepath.Join(plan.Destination, fmt.Sprint(i))
	}
	if statuses := s.uploadDestinationStatuses(plan); statuses != nil {
		t.Fatal("mismatched shell namespace accepted")
	}
}
