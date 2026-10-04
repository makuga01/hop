package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStreamDownloads(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprint(broken), func(t *testing.T) {
			s := localSFTP(t)
			python, e := exec.LookPath("python3")
			if e != nil {
				t.Skip("python3")
			}
			script := batchDownloadPython
			if broken {
				script = strings.ReplaceAll(script, "digest.digest()", "b'x'*32")
			}
			s.batchCommand = func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, python, "-c", script) }
			source, dest := t.TempDir(), t.TempDir()
			plan := CopyPlan{Destination: dest, remoteReadChecked: true}
			for i := 0; i < 160; i++ {
				name := fmt.Sprintf("file %d", i)
				body := strings.Repeat(fmt.Sprint(i), 200)
				if i <= 1 {
					body = strings.Repeat("large synthetic payload", 200000)
				}
				remote := filepath.Join(source, name)
				if i == 1 {
					if e = os.Link(filepath.Join(source, "file 0"), remote); e != nil {
						t.Fatal(e)
					}
				} else {
					os.WriteFile(remote, []byte(body), 0640)
				}
				plan.Files = append(plan.Files, PlannedFile{Remote: remote, Local: filepath.Join(dest, name), Size: uint64(len(body))})
			}
			completed := make([]bool, len(plan.Files))
			p := &batchProgress{embedded: true, files: len(plan.Files)}
			e = streamDownloads(s, plan, Options{}, p, completed)
			if broken {
				if e == nil {
					t.Fatal("corruption accepted")
				}
				entries, _ := os.ReadDir(dest)
				if len(entries) != 0 {
					t.Fatal("corrupt files committed")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			for i, f := range plan.Files {
				got, e := os.ReadFile(f.Local)
				want, _ := os.ReadFile(f.Remote)
				if e != nil || string(got) != string(want) || !completed[i] {
					t.Fatal("mismatch", i, e)
				}
			}
			a, _ := os.Stat(plan.Files[0].Local)
			b, _ := os.Stat(plan.Files[1].Local)
			if os.SameFile(a, b) {
				t.Fatal("reference destinations must remain independent files")
			}
			// A destination created after planning must never be replaced.
			if e = commitStreamFile(plan.Files[0].Local, []byte("replacement"), 0600, false); e == nil {
				t.Fatal("overwritten")
			}
		})
	}
}

func TestLiveSmallBatch(t *testing.T) {
	target := os.Getenv("HOP_SMALL_HOST")
	if target == "" {
		t.Skip("set HOP_SMALL_HOST")
	}
	if !validTarget(target) {
		t.Fatal("invalid host")
	}
	ssh := func(command string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		b, e := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "--", target, command).CombinedOutput()
		if e != nil {
			t.Fatal(e, string(b))
		}
		return strings.TrimSpace(string(b))
	}
	root := ssh("mktemp -d /tmp/hop-small.XXXXXXXXXXXX")
	if !strings.HasPrefix(root, "/tmp/hop-small.") || strings.ContainsAny(strings.TrimPrefix(root, "/tmp/"), "/'\" \n\r\t;$`\\") {
		t.Fatal("invalid temp path")
	}
	defer func() { ssh("rm -rf -- " + shellQuote(root)); t.Log("fixture removed") }()
	count := 512
	if value := os.Getenv("HOP_SMALL_COUNT"); value != "" {
		var err error
		count, err = strconv.Atoi(value)
		if err != nil || count < 1 || count > 25000 {
			t.Fatal("invalid HOP_SMALL_COUNT")
		}
	}
	script := `import pathlib,sys
p=pathlib.Path(sys.argv[1]);data=b'hop-synthetic-data'*1024
for i in range(int(sys.argv[2])): (p/str(i)).write_bytes(data)
(p/'large').write_bytes(data*128)
`
	ssh("python3 -c " + shellQuote(script) + " " + shellQuote(root) + " " + strconv.Itoa(count))
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	h := Host{Target: target, Options: []string{"-o", "BatchMode=yes"}}
	s, e := connectSFTP(h)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for _, backend := range []string{"sftp", "native"} {
		if only := os.Getenv("HOP_SMALL_BACKEND"); only != "" && only != backend {
			continue
		}
		t.Setenv("HOP_TRANSFER_BACKEND", backend)
		destination := t.TempDir()
		replace := os.Getenv("HOP_SMALL_REPLACE") == "1"
		if replace {
			base := filepath.Join(destination, filepath.Base(root))
			if e = os.Mkdir(base, 0700); e != nil {
				t.Fatal(e)
			}
			for i := 0; i < count; i++ {
				if i%3 == 2 {
					continue
				}
				body := strings.Repeat("hop-synthetic-data", 1024)
				if i%3 == 1 {
					body = "old contents"
				}
				if e = os.WriteFile(filepath.Join(base, fmt.Sprint(i)), []byte(body), 0644); e != nil {
					t.Fatal(e)
				}
			}
		}
		plan, e := planBatch(s, []FileItem{{Path: root}}, destination, true, replace)
		if e != nil {
			t.Fatal(e)
		}
		p := &batchProgress{embedded: true, total: plan.Total, files: len(plan.Files), started: time.Now()}
		start := time.Now()
		e = executeBatchPlan(s, h, plan, true, Options{Yes: true, Overwrite: replace}, p)
		elapsed := time.Since(start)
		if e != nil {
			t.Fatal(e)
		}
		for _, f := range plan.Files {
			got, e := os.ReadFile(f.Local)
			size := len("hop-synthetic-data") * 1024
			if filepath.Base(f.Remote) == "large" {
				size *= 128
			}
			if e != nil || string(got) != strings.Repeat("hop-synthetic-data", size/len("hop-synthetic-data")) {
				t.Fatal("verification failed", e)
			}
		}
		t.Logf("SMALL_BATCH backend=%s replace=%t files=%d bytes=%d seconds=%.3f", backend, replace, len(plan.Files), plan.Total, elapsed.Seconds())
	}
}

func TestStreamReplacements(t *testing.T) {
	s := localSFTP(t)
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("python3")
	}
	s.batchCommand = func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, python, "-c", batchDownloadPython)
	}
	source, dest := t.TempDir(), t.TempDir()
	plan := CopyPlan{Destination: dest, remoteReadChecked: true}
	unchanged := map[string]os.FileInfo{}
	for i := 0; i < 48; i++ {
		remote, local := filepath.Join(source, fmt.Sprint(i)), filepath.Join(dest, fmt.Sprint(i))
		body := []byte(fmt.Sprintf("verified new contents %d", i))
		if i == 0 {
			body = nil
		}
		if e = os.WriteFile(remote, body, 0640); e != nil {
			t.Fatal(e)
		}
		replace := i%3 != 2
		if replace {
			old := body
			if i%3 == 1 {
				old = []byte(strings.Repeat("x", len(body)))
			}
			if e = os.WriteFile(local, old, 0640); e != nil {
				t.Fatal(e)
			}
			if i%3 == 0 {
				unchanged[local], e = os.Stat(local)
				if e != nil {
					t.Fatal(e)
				}
			}
		}
		plan.Files = append(plan.Files, PlannedFile{Remote: remote, Local: local, Size: uint64(len(body)), Replace: replace})
	}
	completed := make([]bool, len(plan.Files))
	p := &batchProgress{embedded: true, files: len(plan.Files)}
	if e = streamDownloads(s, plan, Options{Overwrite: true}, p, completed); e != nil {
		t.Fatal(e)
	}
	for i, f := range plan.Files {
		got, e := os.ReadFile(f.Local)
		want, _ := os.ReadFile(f.Remote)
		if e != nil || string(got) != string(want) || !completed[i] {
			t.Fatal("replacement failed", i, e)
		}
		if before := unchanged[f.Local]; before != nil {
			after, _ := os.Stat(f.Local)
			if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
				t.Fatal("identical file rewritten")
			}
		}
	}
	// On a repeat, every request must carry a matching hash; the helper sends
	// self-reference records, never file payloads.
	script := strings.Replace(batchDownloadPython, "for index,name,size,local_hash in requests:", "for index,name,size,local_hash in requests:\n assert local_hash", 1)
	script = strings.Replace(script, "   if base64.b64decode(local_hash,validate=True)==digest:", "   assert base64.b64decode(local_hash,validate=True)==digest\n   if True:", 1)
	s.batchCommand = func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, python, "-c", script) }
	for i := range plan.Files {
		plan.Files[i].Replace = true
	}
	repeated := make([]bool, len(plan.Files))
	if e = streamDownloads(s, plan, Options{Overwrite: true}, &batchProgress{embedded: true}, repeated); e != nil {
		t.Fatal(e)
	}
	for _, done := range repeated {
		if !done {
			t.Fatal("unchanged file not completed")
		}
	}
	if p.backend != "Stream" {
		t.Fatal("stream not used")
	}
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, []byte("keep"), 0600)
	link := filepath.Join(dest, "symlink")
	os.Symlink(victim, link)
	if e = commitStreamFile(link, []byte("replace"), 0600, true); e == nil {
		t.Fatal("symlink overwritten")
	}
	got, _ := os.ReadFile(victim)
	if string(got) != "keep" {
		t.Fatal("symlink target changed")
	}
}

func BenchmarkStreamCommit(b *testing.B) {
	for _, kind := range []string{"fresh", "changed", "identical"} {
		b.Run(kind, func(b *testing.B) {
			root := b.TempDir()
			data := []byte(strings.Repeat("synthetic payload", 1024))
			for n := 0; n < b.N; n++ {
				b.StopTimer()
				paths := make([]string, 512)
				for i := range paths {
					paths[i] = filepath.Join(root, fmt.Sprintf("%d-%d", n, i))
					if kind != "fresh" {
						old := data
						if kind == "changed" {
							old = []byte("old")
						}
						if e := os.WriteFile(paths[i], old, 0640); e != nil {
							b.Fatal(e)
						}
					}
				}
				b.StartTimer()
				if e := parallelFilesN(len(paths), 8, func(i int) error { return commitStreamFile(paths[i], data, 0640, kind != "fresh") }); e != nil {
					b.Fatal(e)
				}
			}
			b.ReportMetric(512, "files/op")
		})
	}
}

func TestStreamStagedDestinationChanges(t *testing.T) {
	for _, kind := range []string{"fresh", "replace", "identical"} {
		t.Run(kind, func(t *testing.T) {
			to := filepath.Join(t.TempDir(), "file")
			if kind != "fresh" {
				data := "old"
				if kind == "identical" {
					data = "new contents"
				}
				if err := os.WriteFile(to, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			p := &pendingStreamFile{to: to, data: []byte("new contents"), mode: 0600, replace: kind != "fresh"}
			defer p.cleanup()
			if err := p.prepare(); err != nil {
				t.Fatal(err)
			}
			if err := syncStreamBatch(context.Background(), []*pendingStreamFile{p}); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(to, []byte("concurrent change must survive"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := p.publish(); err == nil {
				t.Fatal("concurrent destination change accepted")
			}
			got, err := os.ReadFile(to)
			if err != nil || string(got) != "concurrent change must survive" {
				t.Fatal("concurrent destination overwritten", err)
			}
			p.cleanup()
			partial, _ := filepath.Glob(filepath.Join(filepath.Dir(to), ".hop-*.partial"))
			if len(partial) != 0 {
				t.Fatal("staged file leaked")
			}
		})
	}
}

func TestStreamRejectsInvalidReference(t *testing.T) {
	s := localSFTP(t)
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("python3")
	}
	script := `import sys,json,gzip,struct,hashlib
json.load(gzip.GzipFile(fileobj=sys.stdin.buffer,mode='rb'))
sys.stdout.buffer.write(b'HOPFILES1\n');sys.stdout.buffer.flush()
out=gzip.GzipFile(fileobj=sys.stdout.buffer,mode='wb')
out.write(struct.pack('>IQI',0,0,33152));out.write(hashlib.sha256(b'').digest())
out.write(struct.pack('>IQII',1,0,33152|2147483648,999999));out.write(hashlib.sha256(b'').digest());out.close()
`
	s.batchCommand = func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, python, "-c", script) }
	plan := CopyPlan{Destination: t.TempDir(), remoteReadChecked: true}
	for i := 0; i < 40; i++ {
		plan.Files = append(plan.Files, PlannedFile{Local: filepath.Join(plan.Destination, fmt.Sprint(i)), Remote: fmt.Sprintf("/fixture/%d", i)})
	}
	completed := make([]bool, 40)
	p := &batchProgress{embedded: true, files: 40}
	e = streamDownloads(s, plan, Options{}, p, completed)
	if e == nil || !strings.Contains(e.Error(), "invalid batch reference") {
		t.Fatal(e)
	}
	if _, e = os.Stat(plan.Files[1].Local); !os.IsNotExist(e) {
		t.Fatal("invalid reference committed")
	}
	partial, _ := filepath.Glob(filepath.Join(plan.Destination, ".hop-*.partial"))
	if len(partial) != 0 {
		t.Fatal("temporary files leaked")
	}
}

// A helper may have closed stdout successfully while cancellation arrives
// during final local work. Even a clean process exit must not mask cancellation.
func TestStreamCancellationAfterPayload(t *testing.T) {
	s := localSFTP(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3")
	}
	root, dest := t.TempDir(), t.TempDir()
	marker, release := filepath.Join(root, "closed"), filepath.Join(root, "release")
	script := batchDownloadPython + `
import time
sys.stdout.buffer.close()
open(sys.argv[1],'w').close()
while not os.path.exists(sys.argv[2]): time.sleep(.01)
`
	// Deliberately allow the helper to exit normally after context cancellation.
	// This models cancellation after the transport has finished its payload.
	var command *exec.Cmd
	s.batchCommand = func(ctx context.Context) *exec.Cmd {
		command = exec.Command(python, "-c", script, marker, release)
		return command
	}
	plan := CopyPlan{Destination: dest, remoteReadChecked: true}
	for i := 0; i < 32; i++ {
		name := fmt.Sprint(i)
		remote := filepath.Join(root, name)
		if err = os.WriteFile(remote, []byte("payload"), 0600); err != nil {
			t.Fatal(err)
		}
		plan.Files = append(plan.Files, PlannedFile{Remote: remote, Local: filepath.Join(dest, name), Size: 7})
	}
	done := make(chan error, 1)
	progress := &batchProgress{embedded: true}
	go func() { done <- streamDownloads(s, plan, Options{}, progress, make([]bool, 32)) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		progress.mu.Lock()
		allCommitted := progress.doneFiles == len(plan.Files)
		progress.mu.Unlock()
		if _, err = os.Stat(marker); err == nil && allCommitted {
			break
		}
		if time.Now().After(deadline) {
			// Unblock the helper even when reporting a failed test.
			os.WriteFile(release, nil, 0600)
			<-done
			t.Fatal("helper did not finish payload")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.stopExternal()
	if err = os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation masked: %v", err)
	}
	if !command.ProcessState.Success() {
		t.Fatal("fixture helper did not exit cleanly")
	}
	partial, err := filepath.Glob(filepath.Join(dest, ".hop-*.partial"))
	if err != nil || len(partial) != 0 {
		t.Fatal("temporary files leaked", err)
	}
}
