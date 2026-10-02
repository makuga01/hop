package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Creates identical generated 256 MiB fixtures locally and remotely. This
// measures updates only; it deliberately does not claim fresh-copy throughput.
func TestLiveLargeDelta(t *testing.T) {
	target := os.Getenv("HOP_MATRIX_HOST")
	if target == "" {
		t.Skip("set HOP_MATRIX_HOST")
	}
	if !validTarget(target) {
		t.Fatal("invalid host")
	}
	ssh := func(command string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		b, e := exec.CommandContext(ctx, "/usr/bin/ssh", "-o", "BatchMode=yes", "--", target, command).CombinedOutput()
		if e != nil {
			t.Fatal(e, string(b))
		}
		return b
	}
	remote := strings.TrimSpace(string(ssh("mktemp -d /tmp/hop-large.XXXXXXXXXXXX")))
	if !strings.HasPrefix(remote, "/tmp/hop-large.") || strings.ContainsAny(strings.TrimPrefix(remote, "/tmp/"), "/'\" \n\r\t;$`\\") {
		t.Fatal("unexpected fixture path")
	}
	t.Cleanup(func() {
		ssh("rm -rf -- " + shellQuote(remote) + " && test ! -e " + shellQuote(remote))
		t.Log("REMOTE_CLEANUP_OK")
	})
	previous := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = previous }()
	seed := make([]byte, 32)
	rand.Read(seed)
	seedHex := fmt.Sprintf("%x", seed)
	dir := t.TempDir()
	script := `import hashlib,pathlib,sys,struct
p=pathlib.Path(sys.argv[1]); seed=bytes.fromhex(sys.argv[2])
with (p/'seed').open('wb') as f:
 for i in range(256): f.write(hashlib.shake_256(seed+struct.pack('>I',i)).digest(1048576))
(p/'target').mkdir()
`
	if b, e := exec.Command("python3", "-c", script, dir, seedHex).CombinedOutput(); e != nil {
		t.Fatal(e, string(b))
	}
	ssh("python3 -c " + shellQuote(script) + " " + shellQuote(remote) + " " + shellQuote(seedHex))
	copyFile := func(from, to string) {
		t.Helper()
		src, e := os.Open(from)
		if e != nil {
			t.Fatal(e)
		}
		defer src.Close()
		dst, e := os.Create(to)
		if e != nil {
			t.Fatal(e)
		}
		_, e = io.Copy(dst, src)
		ce := dst.Close()
		if e != nil {
			t.Fatal(e)
		}
		if ce != nil {
			t.Fatal(ce)
		}
	}
	digest := func(p string) string {
		t.Helper()
		f, e := os.Open(p)
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		h := sha256.New()
		if _, e := io.Copy(h, f); e != nil {
			t.Fatal(e)
		}
		return fmt.Sprintf("%x", h.Sum(nil))
	}
	seedPath := filepath.Join(dir, "seed")
	if strings.Fields(string(ssh("sha256sum < " + shellQuote(remote+"/seed"))))[0] != digest(seedPath) {
		t.Fatal("generated seeds differ")
	}
	srcDir := filepath.Join(dir, "source")
	os.Mkdir(srcDir, 0700)
	src := filepath.Join(srcDir, "file")
	copyFile(seedPath, src)
	f, e := os.OpenFile(src, os.O_WRONLY, 0)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.WriteAt([]byte(strings.Repeat("X", 4096)), 128<<20)
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	want := digest(src)
	host := Host{Target: target, Options: []string{"-o", "BatchMode=yes"}}
	connections := map[string]*SFTP{}
	for _, backend := range []string{"native", "rsync"} {
		s, e := connectSFTP(host)
		if e != nil {
			t.Fatal(e)
		}
		defer s.Close()
		connections[backend] = s
	}
	for repeat := 0; repeat < 3; repeat++ {
		backends := []string{"native", "rsync"}
		for j := 0; j < 2; j++ {
			backend := backends[(j+repeat)%2]
			s := connections[backend]
			t.Setenv("HOP_TRANSFER_BACKEND", backend)
			ssh("cp " + shellQuote(remote+"/seed") + " " + shellQuote(remote+"/target/file"))
			copyFile(seedPath, filepath.Join(dir, "target", "file"))
			for _, get := range []bool{false, true} {
				source, dest, direction := src, remote+"/target", "upload"
				if get {
					source = remote + "/target/file"
					dest = filepath.Join(dir, "target")
					direction = "download"
				}
				beforeCount, beforeBytes := s.deltaTransfers.Load(), s.deltaReused.Load()
				start := time.Now()
				plan, e := planBatch(s, []FileItem{{Path: source, Regular: true}}, dest, get, true)
				if e != nil {
					t.Fatal(e)
				}
				p := &batchProgress{embedded: true, total: plan.Total, files: 1, started: time.Now()}
				if e := executeBatchPlan(s, host, plan, get, Options{Overwrite: true}, p); e != nil {
					t.Fatal(backend, direction, e)
				}
				elapsed := time.Since(start)
				if backend == "native" && (s.rsyncCap != nil || s.deltaTransfers.Load() != beforeCount+1) {
					t.Fatal("wrong native backend")
				}
				if backend == "rsync" && s.rsyncCap == nil {
					t.Fatal("comparator fell back")
				}
				var actual string
				if get {
					actual = digest(filepath.Join(dest, "file"))
				} else {
					actual = strings.Fields(string(ssh("sha256sum < " + shellQuote(remote+"/target/file"))))[0]
				}
				if actual != want {
					t.Fatal("checksum differs")
				}
				t.Logf("LARGE backend=%s direction=%s repeat=%d bytes=%d changed_bytes=4096 milliseconds=%.1f reused_bytes=%d sha256=PASS", backend, direction, repeat+1, 256<<20, float64(elapsed.Microseconds())/1000, s.deltaReused.Load()-beforeBytes)
			}
		}
	}
}
