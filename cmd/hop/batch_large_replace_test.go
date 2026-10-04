package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamLargeReplacements(t *testing.T) {
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	s := localSFTP(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3")
	}
	s.batchCommand = func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, python, "-c", batchDownloadPython)
	}
	source, dest := t.TempDir(), t.TempDir()
	plan := CopyPlan{Destination: dest, remoteReadChecked: true}
	large := bytes.Repeat([]byte("verified large data"), 128000)
	for i := 0; i < 40; i++ {
		name := fmt.Sprint(i)
		body := []byte(name)
		if i < 4 {
			body = large
		}
		remote, local := filepath.Join(source, name), filepath.Join(dest, name)
		if i == 2 || i == 3 {
			// The fresh copy of the deferred file must receive data, not a
			// reference to an existing destination that has not been updated.
			if err := os.Link(filepath.Join(source, fmt.Sprint(i-2)), remote); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(remote, body, 0640); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			old := append([]byte(nil), body...)
			if i == 1 {
				old[len(old)/2] ^= 1
			}
			if err := os.WriteFile(local, old, 0600); err != nil {
				t.Fatal(err)
			}
		}
		plan.Files = append(plan.Files, PlannedFile{Local: local, Remote: remote, Size: uint64(len(body)), Replace: i < 2})
		plan.Total += uint64(len(body))
	}
	before, _ := os.Stat(plan.Files[0].Local)
	oldChanged, _ := os.ReadFile(plan.Files[1].Local)
	completed := make([]bool, len(plan.Files))
	p := &batchProgress{embedded: true}
	if err := streamDownloads(s, plan, Options{Overwrite: true}, p, completed); err != nil {
		t.Fatal(err)
	}
	for i, done := range completed {
		if done != (i != 1) {
			t.Fatalf("unexpected completion for file %d: %v", i, done)
		}
		got, err := os.ReadFile(plan.Files[i].Local)
		want, _ := os.ReadFile(plan.Files[i].Remote)
		if i == 1 {
			want = oldChanged
		}
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("incorrect result", i, err)
		}
	}
	after, _ := os.Stat(plan.Files[0].Local)
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) || after.Mode().Perm() != 0640 {
		t.Fatal("unchanged large file was rewritten or permissions lost")
	}
	// Complete the deferred file using the existing copy path.
	remaining := plan
	remaining.Files = []PlannedFile{plan.Files[1]}
	remaining.Total = plan.Files[1].Size
	if err := copyPlannedFiles(s, Host{Target: "fixture"}, remaining, true, Options{Overwrite: true, Yes: true, SkipReview: true}, p); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(plan.Files[1].Local)
	if err != nil || !bytes.Equal(got, large) {
		t.Fatal("deferred replacement did not complete", err)
	}
}

func TestVerifiedStreamDestinationRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	to, moved := filepath.Join(root, "destination"), filepath.Join(root, "moved")
	if err := os.WriteFile(to, []byte("verified"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(to)
	if err := os.Rename(to, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, to); err != nil {
		t.Fatal(err)
	}
	p := &pendingStreamFile{to: to, original: before, mode: 0644, replace: true}
	if err := p.prepare(); err == nil {
		t.Fatal("verified destination symlink accepted")
	}
	after, _ := os.Stat(moved)
	if after.Mode().Perm() != 0600 {
		t.Fatal("symlink target permissions changed")
	}
}

// Deferred records must never turn fresh or small files into successful skips.
func TestStreamRejectsInvalidDeferredFile(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3")
	}
	for _, kind := range []string{"fresh", "small", "reference"} {
		t.Run(kind, func(t *testing.T) {
			s := localSFTP(t)
			dest := t.TempDir()
			size := uint64(2 << 20)
			replace := kind != "fresh"
			if kind == "small" {
				size = 1
			}
			flags := uint32(0x40000000)
			if kind == "reference" {
				flags |= 0x80000000
			}
			plan := CopyPlan{Destination: dest, remoteReadChecked: true}
			for i := 0; i < 40; i++ {
				f := PlannedFile{Local: filepath.Join(dest, fmt.Sprint(i)), Remote: fmt.Sprintf("/fixture/%d", i), Size: size, Replace: replace}
				if replace {
					if err := os.WriteFile(f.Local, make([]byte, int(size)), 0600); err != nil {
						t.Fatal(err)
					}
				}
				plan.Files = append(plan.Files, f)
			}
			script := fmt.Sprintf(`import sys,json,gzip,struct
json.load(gzip.GzipFile(fileobj=sys.stdin.buffer,mode='rb'))
sys.stdout.buffer.write(b'HOPFILES1\n');sys.stdout.buffer.flush()
out=gzip.GzipFile(fileobj=sys.stdout.buffer,mode='wb')
out.write(struct.pack('>IQI',0,%d,33152|%d));out.close()
`, size, flags)
			s.batchCommand = func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, python, "-c", script) }
			completed := make([]bool, 40)
			err := streamDownloads(s, plan, Options{Overwrite: true}, &batchProgress{embedded: true}, completed)
			if err == nil || !strings.Contains(err.Error(), "invalid deferred batch file") {
				t.Fatal(err)
			}
			for _, done := range completed {
				if done {
					t.Fatal("invalid deferred file completed")
				}
			}
		})
	}
}
