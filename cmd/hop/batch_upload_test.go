package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func uploadFixture(t *testing.T, script string) (*SFTP, CopyPlan) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3")
	}
	s := localSFTP(t)
	s.uploadCommand = func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, python, "-c", script) }
	source, dest := t.TempDir(), t.TempDir()
	plan := CopyPlan{Destination: dest}
	for i := 0; i < 96; i++ {
		name := fmt.Sprintf("file %d\nλ", i)
		body := bytes.Repeat([]byte(name), i*91)
		if i == 2 {
			body = bytes.Repeat([]byte("large fixture"), 200000)
		}
		local, remote := filepath.Join(source, name), filepath.Join(dest, name)
		if err := os.WriteFile(local, body, 0640); err != nil {
			t.Fatal(err)
		}
		plan.Files = append(plan.Files, PlannedFile{Local: local, Remote: remote, Size: uint64(len(body))})
		plan.Total += uint64(len(body))
	}
	return s, plan
}

func TestStreamUploads(t *testing.T) {
	s, plan := uploadFixture(t, batchUploadPython)
	for _, replace := range []bool{false, true} {
		for i := range plan.Files {
			plan.Files[i].Replace = replace
		}
		if replace {
			for i, f := range plan.Files {
				if i != 2 {
					if err := os.WriteFile(f.Remote, []byte("previous"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		completed := make([]bool, len(plan.Files))
		if err := streamUploads(s, plan, Options{Overwrite: replace}, &batchProgress{embedded: true}, completed); err != nil {
			t.Fatal(err)
		}
		for i, f := range plan.Files {
			if completed[i] != (i != 2 || !replace) {
				t.Fatal("incorrect completion", i)
			}
			got, err := os.ReadFile(f.Remote)
			want, _ := os.ReadFile(f.Local)
			info, _ := os.Stat(f.Remote)
			if err != nil || !bytes.Equal(got, want) || info.Mode().Perm() != 0640 {
				t.Fatal("incorrect uploaded file", i, err)
			}
		}
	}
	partial, _ := filepath.Glob(filepath.Join(plan.Destination, ".hop-*.partial"))
	if len(partial) > 0 {
		t.Fatal("partial files leaked")
	}
}

func TestStreamUploadFailurePreservesDestination(t *testing.T) {
	for _, kind := range []string{"corrupt", "exists", "symlink", "cancel", "commit-change", "source-change", "namespace", "sync-failure"} {
		t.Run(kind, func(t *testing.T) {
			script := batchUploadPython
			if kind == "sync-failure" {
				script = strings.Replace(script, "def sync(j):", "def sync(j):\n if j['index']==0: raise OSError('injected sync failure')", 1)
			}
			if kind == "namespace" {
				script = strings.Replace(script, "if os.path.realpath(root)!=canonical:", "if True:", 1)
			}
			if kind == "corrupt" {
				script = strings.Replace(script, "if read(32)!=digest.digest():", "if read(32)!=b'x'*32:", 1)
			}
			if kind == "commit-change" {
				script = strings.Replace(script, "def commit():", "def commit():\n if pending:\n  with open(os.path.join(pending[0]['directory'],pending[0]['name']),'wb') as changed: changed.write(b'concurrent')", 1)
			}
			if kind == "source-change" {
				script = strings.Replace(script, "sys.stdout.buffer.write(b'HOPUPG1", "import time;time.sleep(.2)\nsys.stdout.buffer.write(b'HOPUPG1", 1)
			}
			s, plan := uploadFixture(t, script)
			first := plan.Files[0].Remote
			if kind == "symlink" {
				target := filepath.Join(t.TempDir(), "keep")
				os.WriteFile(target, []byte("keep"), 0600)
				if err := os.Symlink(target, first); err != nil {
					t.Fatal(err)
				}
				plan.Files[0].Replace = true
			} else {
				if err := os.WriteFile(first, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				plan.Files[0].Replace = kind != "exists"
			}
			if kind == "source-change" {
				os.WriteFile(plan.Files[0].Local, []byte("unexpected size"), 0600)
			}
			if kind == "cancel" {
				s.stopExternal()
			}
			completed := make([]bool, len(plan.Files))
			err := streamUploads(s, plan, Options{Overwrite: true}, &batchProgress{embedded: true}, completed)
			if err == nil {
				t.Fatal("unsafe upload succeeded")
			}
			got, e := os.ReadFile(first)
			want := "keep"
			if kind == "commit-change" {
				want = "concurrent"
			}
			if e != nil || string(got) != want {
				t.Fatal("destination overwritten", e)
			}
			if completed[0] {
				t.Fatal("failed file marked complete")
			}
			partial, _ := filepath.Glob(filepath.Join(plan.Destination, ".hop-*.partial"))
			if len(partial) > 0 {
				t.Fatal("partial files leaked", len(partial))
			}
		})
	}
}

func TestStreamUploadCapabilityFallback(t *testing.T) {
	s, plan := uploadFixture(t, "raise SystemExit(1)")
	completed := make([]bool, len(plan.Files))
	if err := streamUploads(s, plan, Options{}, &batchProgress{embedded: true}, completed); err != nil {
		t.Fatal(err)
	}
	for _, done := range completed {
		if done {
			t.Fatal("unavailable helper completed a file")
		}
	}
	files, _ := os.ReadDir(plan.Destination)
	if len(files) != 0 {
		t.Fatal("fallback wrote files")
	}
}

// Synthetic data only, isolated local and remote directories. No saved paths.
func TestLiveUploadStream(t *testing.T) {
	host := os.Getenv("HOP_UPLOAD_HOST")
	if host == "" {
		t.Skip("set HOP_UPLOAD_HOST for an authorized live test")
	}
	if !validTarget(host) {
		t.Fatal("invalid test host")
	}
	ssh := func(command string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		b, e := exec.CommandContext(ctx, "/usr/bin/ssh", "-o", "BatchMode=yes", "--", host, command).CombinedOutput()
		if e != nil {
			t.Fatal(e, string(b))
		}
		return b
	}
	root := strings.TrimSpace(string(ssh("mktemp -d /tmp/hop-upload.XXXXXXXXXXXX")))
	if !strings.HasPrefix(root, "/tmp/hop-upload.") || strings.ContainsAny(strings.TrimPrefix(root, "/tmp/"), "/'\" \n\r\t;$`\\") {
		t.Fatal("unexpected fixture directory")
	}
	defer func() {
		ssh("rm -rf -- " + shellQuote(root) + " && test ! -e " + shellQuote(root))
		t.Log("REMOTE_CLEANUP_OK")
	}()
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	source := t.TempDir()
	var roots []FileItem
	expected := map[string]string{}
	count := 2048
	if value := os.Getenv("HOP_UPLOAD_COUNT"); value != "" {
		var err error
		count, err = strconv.Atoi(value)
		if err != nil || count < 1 || count > 25000 {
			t.Fatal("invalid upload count")
		}
	}
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("file-%04d", i)
		body := bytes.Repeat([]byte(fmt.Sprintf("fixture record %04d has repeatable text\n", i)), 400)
		if os.Getenv("HOP_UPLOAD_RANDOM") == "1" {
			if _, err := rand.Read(body); err != nil {
				t.Fatal(err)
			}
		}
		file := filepath.Join(source, name)
		if err := os.WriteFile(file, body, 0640); err != nil {
			t.Fatal(err)
		}
		expected[name] = fmt.Sprintf("%x", sha256.Sum256(body))
		roots = append(roots, FileItem{Path: file, Regular: true})
	}
	h := Host{Target: host, Options: []string{"-o", "BatchMode=yes"}}
	s, err := connectSFTP(h)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, backend := range []string{"sftp", "native"} {
		if selected := os.Getenv("HOP_UPLOAD_BACKEND"); selected != "" && selected != backend {
			continue
		}
		dest := root + "/" + backend
		if err := s.Mkdir(dest, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOP_TRANSFER_BACKEND", backend)
		start := time.Now()
		plan, err := planBatch(s, roots, dest, false, false)
		if err != nil {
			t.Fatal(err)
		}
		planning := time.Since(start)
		start = time.Now()
		p := &batchProgress{embedded: true, total: plan.Total, files: len(plan.Files), started: start}
		if err := executeBatchPlan(s, h, plan, false, Options{}, p); err != nil {
			t.Fatal(err)
		}
		elapsed := time.Since(start)
		output := ssh("cd " + shellQuote(dest) + " && sha256sum -- file-*")
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		if len(lines) != len(expected) {
			t.Fatal("wrong remote count")
		}
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) != 2 || expected[fields[1]] != fields[0] {
				t.Fatal("remote checksum mismatch")
			}
		}
		t.Logf("UPLOAD backend=%s files=%d bytes=%d plan_seconds=%.3f copy_seconds=%.3f verified=all", backend, len(plan.Files), plan.Total, planning.Seconds(), elapsed.Seconds())
	}
}

func TestUploadSourceChangeBeforeChecksum(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	os.WriteFile(source, bytes.Repeat([]byte("x"), 1<<20), 0600)
	item := PlannedFile{Local: source, Size: 1 << 20}
	p := (&batchProgress{embedded: true}).startFile(item, false, "")
	changed := false
	err := writeUploadStreamFile(context.Background(), io.Discard, 0, item, p, func() {
		if !changed {
			changed = true
			os.WriteFile(source, []byte("changed"), 0600)
		}
	})
	if err == nil {
		t.Fatal("changed source accepted")
	}
}

func TestStreamUploadCancellationCleansStaging(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "staged")
	script := strings.Replace(batchUploadPython, "pending.append(j)", "pending.append(j)", 1)
	script = strings.Replace(script, "j['temp']=temp;j['file']=os.fdopen(fd,'wb')", "j['temp']=temp;j['file']=os.fdopen(fd,'wb')\n  open("+strconv.Quote(marker)+",'w').close()", 1)
	s, plan := uploadFixture(t, script)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.externalOnce.Do(func() { s.externalCtx = ctx; s.externalCancel = cancel })
	finished := make(chan error, 1)
	go func() {
		finished <- streamUploads(s, plan, Options{}, &batchProgress{embedded: true}, make([]bool, len(plan.Files)))
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not stage")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("canceled upload succeeded")
		}
	case <-time.After(7 * time.Second):
		t.Fatal("cancel hung")
	}
	partial, _ := filepath.Glob(filepath.Join(plan.Destination, ".hop-*.partial"))
	if len(partial) != 0 {
		t.Fatal("canceled upload leaked staging files")
	}
}

func TestStreamUploadSingleLargeFile(t *testing.T) {
	s, plan := uploadFixture(t, batchUploadPython)
	f := plan.Files[0]
	body := bytes.Repeat([]byte("large"), 2<<20)
	os.WriteFile(f.Local, body, 0600)
	f.Size = uint64(len(body))
	plan.Files = []PlannedFile{f}
	plan.Total = f.Size
	completed := make([]bool, 1)
	if err := streamUploads(s, plan, Options{}, &batchProgress{embedded: true}, completed); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(f.Remote)
	if err != nil || !completed[0] || !bytes.Equal(got, body) {
		t.Fatal("large stream failed", err)
	}
}
