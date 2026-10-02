package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func rsyncFixture(t *testing.T) (*SFTP, string) {
	t.Helper()
	binary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync not installed")
	}
	version, err := exec.Command(binary, "--version").Output()
	if err != nil || !usableRsync(version) {
		t.Skip("rsync 3.1+ required")
	}
	s := localSFTP(t)
	dir := filepath.Join(t.TempDir(), "ssh ' helper")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "ssh")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nshift\nexec \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	s.rsyncOnce.Do(func() { s.rsyncCap = &rsyncCapability{binary, rsyncRSH([]string{shim})} })
	t.Setenv("HOP_TRANSFER_BACKEND", "rsync")
	return s, binary
}
func rsyncTestPlan(t *testing.T, s *SFTP, src, dest string, get, overwrite bool) (CopyPlan, *batchProgress) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	var roots []FileItem
	for _, entry := range entries {
		roots = append(roots, FileItem{Path: filepath.Join(src, entry.Name()), Regular: true})
	}
	plan, err := planBatch(s, roots, dest, get, overwrite)
	if err != nil {
		t.Fatal(err)
	}
	return plan, &batchProgress{embedded: true, total: plan.Total, files: len(plan.Files)}
}
func TestRsyncStagedRoundTripAndDelta(t *testing.T) {
	s, _ := rsyncFixture(t)
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	source, remote, dest := t.TempDir(), t.TempDir(), t.TempDir()
	for _, name := range []string{"-leading", "space 'quote' $literal`\nnew-line", "readonly", "zero"} {
		payload := bytes.Repeat([]byte(name), 1024)
		if name == "zero" {
			payload = nil
		}
		mode := os.FileMode(0640)
		if name == "readonly" {
			mode = 0400
		}
		if err := os.WriteFile(filepath.Join(source, name), payload, mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, get := range []bool{false, true} {
		src, dst := source, remote
		if get {
			src, dst = remote, dest
		}
		for _, overwrite := range []bool{false, true} {
			if overwrite {
				file := filepath.Join(src, "-leading")
				f, err := os.OpenFile(file, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteAt([]byte("EDIT"), 32)
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			plan, p := rsyncTestPlan(t, s, src, dst, get, overwrite)
			if err := executeBatchPlan(s, Host{Target: "fixture"}, plan, get, Options{Overwrite: overwrite}, p); err != nil {
				t.Fatal(err)
			}
			if p.doneFiles != 4 || p.current != 0 || p.completed != plan.Total {
				t.Fatalf("bad progress: %d %d %d", p.doneFiles, p.current, p.completed)
			}
			for _, f := range plan.Files {
				from, to := f.Local, f.Remote
				if get {
					from, to = to, from
				}
				want, _ := os.ReadFile(from)
				got, err := os.ReadFile(to)
				if err != nil || !bytes.Equal(want, got) {
					t.Fatal("content mismatch", to, err)
				}
				a, _ := os.Stat(from)
				b, _ := os.Stat(to)
				if a.Mode().Perm() != b.Mode().Perm() {
					t.Fatal("mode mismatch", to)
				}
			}
			leftovers, _ := filepath.Glob(filepath.Join(dst, ".hop-rsync-*"))
			if len(leftovers) != 0 {
				t.Fatal(leftovers)
			}
		}
	}
}
func TestRsyncDestinationRaceAndProcessFailure(t *testing.T) {
	for _, get := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{true: "get", false: "send"}[get], map[bool]string{true: "process-fails", false: "racing-creator"}[fail]}, "/"), func(t *testing.T) {
				s, binary := rsyncFixture(t)
				src, dest := t.TempDir(), t.TempDir()
				if err := os.WriteFile(filepath.Join(src, "file"), []byte("payload"), 0600); err != nil {
					t.Fatal(err)
				}
				plan, p := rsyncTestPlan(t, s, src, dest, get, false)
				script := "#!/bin/sh\nexit 23\n"
				if !fail {
					script = "#!/bin/sh\n'" + strings.ReplaceAll(binary, "'", "'\\''") + "' \"$@\" || exit $?\nprintf 'racing creator' > '" + filepath.Join(dest, "file") + "'\n"
				}
				wrapper := filepath.Join(t.TempDir(), "rsync")
				if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				s.rsyncCap.binary = wrapper
				handled, err := tryRsyncBatch(s, Host{Target: "fixture"}, plan, get, Options{}, p, make([]bool, 1))
				if !handled || err == nil {
					t.Fatal("expected staged failure", handled, err)
				}
				got, readErr := os.ReadFile(filepath.Join(dest, "file"))
				if fail {
					if !os.IsNotExist(readErr) {
						t.Fatal("failed process committed a destination")
					}
				} else {
					var conflict *DestinationExistsError
					if !errors.As(err, &conflict) || string(got) != "racing creator" {
						t.Fatal("clobbered concurrent creator", err)
					}
				}
				entries, _ := os.ReadDir(dest)
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".hop-rsync-") {
						t.Fatal("staging leaked")
					}
				}
			})
		}
	}
}
func TestRsyncEligibilityAndFallback(t *testing.T) {
	t.Setenv("HOP_TRANSFER_BACKEND", "")
	plan := CopyPlan{Files: []PlannedFile{{Local: "/a/f", Remote: "/b/f", Size: 8 << 20, Replace: true}}}
	if rsyncCandidate(plan, false, Options{}) || rsyncCandidate(plan, false, Options{Overwrite: true}) {
		t.Fatal("overwrite policy")
	}
	t.Setenv("HOP_TRANSFER_BACKEND", "rsync")
	if rsyncCandidate(plan, false, Options{DryRun: true}) {
		t.Fatal("dry run may execute rsync")
	}
	if _, _, _, ok := rsyncLayout(plan, false); !ok {
		t.Fatal("flat plan rejected")
	}
	plan.Files = append(plan.Files, PlannedFile{Local: "/a/sub/f", Remote: "/b/sub/f"})
	if _, _, _, ok := rsyncLayout(plan, false); ok {
		t.Fatal("nested layout accepted")
	}
	if (&SFTP{}).detectRsync(Host{Target: "fixture"}) != nil {
		t.Fatal("non-SSH connection probed")
	}
	for _, v := range []string{"rsync  version 2.6.9 protocol version 29", "not rsync"} {
		if usableRsync([]byte(v)) {
			t.Fatal(v)
		}
	}
	if !usableRsync([]byte("rsync  version 3.4.1  protocol version 32")) {
		t.Fatal("modern rsync rejected")
	}
	if rsyncRemote("user@2001:db8::1", "/a b") != "user@[2001:db8::1]:/a b/" {
		t.Fatal("IPv6 quoting")
	}
}
func TestRsyncProgressAndCancellation(t *testing.T) {
	p := &batchProgress{total: 20000}
	w := &rsyncProgress{p: p}
	w.Write([]byte("\r  12,345  61% 123kB/s"))
	w.Write([]byte("\r"))
	if p.current != 12345 {
		t.Fatal(p.current)
	}
	s := &SFTP{}
	ctx := s.externalContext()
	s.stopExternal()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("external process context not canceled")
	}
}

func TestRsyncSourceChangeAndCancellation(t *testing.T) {
	for _, get := range []bool{false, true} {
		t.Run(map[bool]string{true: "get", false: "send"}[get], func(t *testing.T) {
			s, binary := rsyncFixture(t)
			src, dest := t.TempDir(), t.TempDir()
			source := filepath.Join(src, "file")
			if err := os.WriteFile(source, []byte("payload"), 0600); err != nil {
				t.Fatal(err)
			}
			plan, p := rsyncTestPlan(t, s, src, dest, get, false)
			script := "#!/bin/sh\n'" + strings.ReplaceAll(binary, "'", "'\\''") + "' \"$@\" || exit $?\nprintf 'changed' >> '" + source + "'\n"
			wrapper := filepath.Join(t.TempDir(), "rsync")
			os.WriteFile(wrapper, []byte(script), 0700)
			s.rsyncCap.binary = wrapper
			handled, err := tryRsyncBatch(s, Host{Target: "fixture"}, plan, get, Options{}, p, make([]bool, 1))
			if !handled || err == nil || !strings.Contains(err.Error(), "source changed") {
				t.Fatal(handled, err)
			}
			entries, _ := os.ReadDir(dest)
			if len(entries) != 0 {
				t.Fatal("source-change failure left files", entries)
			}
		})
	}
	t.Run("cancel", func(t *testing.T) {
		s, _ := rsyncFixture(t)
		src, dest := t.TempDir(), t.TempDir()
		os.WriteFile(filepath.Join(src, "file"), []byte("payload"), 0600)
		plan, p := rsyncTestPlan(t, s, src, dest, false, false)
		wrapper := filepath.Join(t.TempDir(), "rsync")
		os.WriteFile(wrapper, []byte("#!/bin/sh\nsleep 30\n"), 0700)
		s.rsyncCap.binary = wrapper
		timer := time.AfterFunc(100*time.Millisecond, s.stopExternal)
		defer timer.Stop()
		start := time.Now()
		handled, err := tryRsyncBatch(s, Host{Target: "fixture"}, plan, false, Options{}, p, make([]bool, 1))
		if !handled || err == nil || time.Since(start) > 3*time.Second {
			t.Fatal("cancel failed", handled, err)
		}
		entries, _ := os.ReadDir(dest)
		if len(entries) != 0 {
			t.Fatal("canceled transfer left staging", entries)
		}
	})
}

func TestRsyncMissingCapabilityUsesSFTP(t *testing.T) {
	s := localSFTP(t)
	t.Setenv("HOP_TRANSFER_BACKEND", "rsync")
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	src, dest := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(src, "file"), []byte("fallback"), 0600)
	plan, p := rsyncTestPlan(t, s, src, dest, false, false)
	if err := executeBatchPlan(s, Host{Target: "fixture"}, plan, false, Options{}, p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dest, "file"))
	if err != nil || string(b) != "fallback" {
		t.Fatal(err, string(b))
	}
}

func TestRsyncStagingNameCollisions(t *testing.T) {
	s := localSFTP(t)
	for _, get := range []bool{false, true} {
		stage := t.TempDir()
		// A duplicate spelling must fail before the backend can merge its files.
		if err := s.reserveRsyncNames(stage, []string{"same", "same"}, get, 2); err == nil {
			t.Fatal("duplicate names accepted")
		}
		stage = t.TempDir()
		if err := s.reserveRsyncNames(stage, []string{"first", "second"}, get, 2); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(stage)
		if err != nil || len(entries) != 0 {
			t.Fatal("placeholders retained", err)
		}
	}
}

func TestRsyncSingleFileRoutingPreservesNames(t *testing.T) {
	s := localSFTP(t)
	root := t.TempDir()
	source := filepath.Join(root, "file")
	for _, target := range []string{root, filepath.Join(root, "file")} {
		got, ok := rsyncUploadDirectory(s, source, target)
		if !ok || got != root {
			t.Fatal(got, ok)
		}
		got, ok = rsyncDownloadDirectory(source, target)
		if !ok || got != root {
			t.Fatal(got, ok)
		}
	}
	if _, ok := rsyncUploadDirectory(s, source, filepath.Join(root, "renamed")); ok {
		t.Fatal("upload rename changed")
	}
	if _, ok := rsyncDownloadDirectory(source, filepath.Join(root, "renamed")); ok {
		t.Fatal("download rename changed")
	}
	t.Setenv("HOP_TRANSFER_BACKEND", "sftp")
	if rsyncSingleCandidate(Options{Overwrite: true}, 8<<20) {
		t.Fatal("ignored SFTP override")
	}
}
