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
	"strconv"
	"strings"
	"testing"
	"time"
)

// Explicit opt-in only: creates generated fixtures in a unique remote /tmp
// directory and removes that directory on completion. Never uses saved paths.
// HOP_LIVE_HOST=benchmark-host go test ./cmd/hop -run '^TestLiveTransfer$' -v -timeout 20m
func TestLiveTransfer(t *testing.T) {
	target := os.Getenv("HOP_LIVE_HOST")
	if target == "" {
		t.Skip("set HOP_LIVE_HOST to run an authorized live benchmark")
	}
	if !validTarget(target) {
		t.Fatal("invalid benchmark host")
	}
	ssh := func(command string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		return exec.CommandContext(ctx, "/usr/bin/ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "--", target, command).CombinedOutput()
	}
	b, err := ssh("mktemp -d /tmp/hop-benchmark.XXXXXXXXXXXX")
	if err != nil {
		t.Fatalf("create remote fixture: %v: %s", err, b)
	}
	remote := strings.TrimSpace(string(b))
	if !strings.HasPrefix(remote, "/tmp/hop-benchmark.") || strings.ContainsAny(remote, "'\" \n\r\t;$`\\") || strings.Contains(remote[5:], "/") {
		t.Fatal("unexpected temporary directory response")
	}
	t.Cleanup(func() {
		if b, err := ssh("rm -rf -- '" + remote + "' && test ! -e '" + remote + "'"); err != nil {
			t.Errorf("remote fixture cleanup failed at %s: %v: %s", remote, err, b)
		} else {
			t.Log("REMOTE_CLEANUP_OK")
		}
	})
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	host := Host{Target: target, Options: []string{"-o", "BatchMode=yes"}}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	s, err := connectSFTPContext(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t.Logf("SSH_CONNECT_MS=%.1f", float64(time.Since(start).Microseconds())/1000)
	start = time.Now()
	for i := 0; i < 10; i++ {
		if _, err := s.Stat(remote, true); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("SFTP_STAT_MEAN_MS=%.1f", float64(time.Since(start).Microseconds())/10000)
	smallCount := 1024
	if v := os.Getenv("HOP_LIVE_SMALL_FILES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1024 {
			t.Fatal("invalid small-file count")
		}
		smallCount = n
	}
	largeBytes := int64(256 << 20)
	if v := os.Getenv("HOP_LIVE_LARGE_MIB"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 256 {
			t.Fatal("invalid large-file size")
		}
		largeBytes = int64(n) << 20
	}
	for _, count := range []int{1, smallCount} {
		source, dest := t.TempDir(), t.TempDir()
		remoteDest := fmt.Sprintf("%s/files-%d", remote, count)
		if err := s.Mkdir(remoteDest, 0700); err != nil {
			t.Fatal(err)
		}
		expected := map[string]string{}
		var roots []FileItem
		for i := 0; i < count; i++ {
			name := fmt.Sprintf("file-%04d", i)
			local := filepath.Join(source, name)
			f, err := os.Create(local)
			if err != nil {
				t.Fatal(err)
			}
			size := int64(4096)
			if count == 1 {
				size = largeBytes
			}
			hash := sha256.New()
			_, err = io.CopyN(io.MultiWriter(f, hash), rand.Reader, size)
			ce := f.Close()
			if err != nil {
				t.Fatal(err)
			}
			if ce != nil {
				t.Fatal(ce)
			}
			expected[name] = fmt.Sprintf("%x", hash.Sum(nil))
			roots = append(roots, FileItem{Path: local, Regular: true})
		}
		for _, get := range []bool{false, true} {
			direction := "upload"
			destination := remoteDest
			if get {
				direction = "download"
				destination = dest
				for i := range roots {
					roots[i].Path = remoteDest + "/" + filepath.Base(roots[i].Path)
				}
			}
			start = time.Now()
			plan, err := planBatch(s, roots, destination, get, false)
			if err != nil {
				t.Fatal(err)
			}
			scan := time.Since(start)
			p := &batchProgress{embedded: true, total: plan.Total, files: len(plan.Files), started: time.Now()}
			err = executeBatchPlan(s, host, plan, get, Options{}, p)
			elapsed := time.Since(start)
			if err != nil {
				t.Fatal(err)
			}
			if !get {
				output, err := ssh("cd '" + remoteDest + "' && sha256sum -- file-*")
				if err != nil {
					t.Fatalf("remote checksums: %v: %s", err, output)
				}
				seen := map[string]bool{}
				for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
					fields := strings.Fields(line)
					if len(fields) != 2 || expected[fields[1]] != fields[0] {
						t.Fatalf("remote checksum mismatch: %s", line)
					}
					seen[fields[1]] = true
				}
				if len(seen) != count {
					t.Fatalf("verified %d/%d remote files", len(seen), count)
				}
			} else {
				for name, want := range expected {
					f, err := os.Open(filepath.Join(dest, name))
					if err != nil {
						t.Fatal(err)
					}
					hash := sha256.New()
					_, err = io.Copy(hash, f)
					f.Close()
					if err != nil || fmt.Sprintf("%x", hash.Sum(nil)) != want {
						t.Fatalf("local checksum mismatch: %s: %v", name, err)
					}
				}
			}
			t.Logf("LIVE_RESULT direction=%s files=%d bytes=%d scan_ms=%.1f total_ms=%.1f MiB_s=%.2f sha256=PASS", direction, count, plan.Total, float64(scan.Microseconds())/1000, float64(elapsed.Microseconds())/1000, float64(plan.Total)/(1<<20)/elapsed.Seconds())
		}
		if count == 1 && (os.Getenv("HOP_LIVE_RSYNC_DELTA") == "1" || os.Getenv("HOP_LIVE_DELTA") == "1") {
			name := "file-0000"
			f, err := os.OpenFile(filepath.Join(source, name), os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			edit := make([]byte, 4096)
			if _, err := rand.Read(edit); err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteAt(edit, largeBytes/2); err != nil {
				t.Fatal(err)
			}
			f.Close()
			expectedHash, err := os.ReadFile(filepath.Join(source, name))
			if err != nil {
				t.Fatal(err)
			}
			wanted := fmt.Sprintf("%x", sha256.Sum256(expectedHash))
			for _, get := range []bool{false, true} {
				src, dst, direction := filepath.Join(source, name), remoteDest, "upload"
				if get {
					src, dst, direction = remoteDest+"/"+name, dest, "download"
				}
				beforeNative := s.deltaTransfers.Load()
				started := time.Now()
				plan, err := planBatch(s, []FileItem{{Path: src, Regular: true}}, dst, get, true)
				if err != nil {
					t.Fatal(err)
				}
				progress := &batchProgress{embedded: true, total: plan.Total, files: 1, started: time.Now()}
				if err := executeBatchPlan(s, host, plan, get, Options{Overwrite: true}, progress); err != nil {
					t.Fatal(err)
				}
				elapsed := time.Since(started)
				actual := ""
				if get {
					b, err := os.ReadFile(filepath.Join(dest, name))
					if err != nil {
						t.Fatal(err)
					}
					actual = fmt.Sprintf("%x", sha256.Sum256(b))
				} else {
					output, err := ssh("sha256sum -- '" + remoteDest + "/" + name + "'")
					if err != nil {
						t.Fatal(err)
					}
					actual = strings.Fields(string(output))[0]
				}
				if actual != wanted {
					t.Fatal("delta checksum mismatch")
				}
				if os.Getenv("HOP_LIVE_RSYNC_DELTA") == "1" && s.rsyncCap == nil {
					t.Fatal("live delta did not detect rsync")
				}
				if os.Getenv("HOP_LIVE_DELTA") == "1" {
					if s.rsyncCap != nil || s.deltaTransfers.Load() != beforeNative+1 {
						t.Fatal("native delta not used, or rsync detected")
					}
					t.Logf("NATIVE_DELTA completed=%d reused_bytes=%d", s.deltaTransfers.Load(), s.deltaReused.Load())
				}
				t.Logf("LIVE_DELTA direction=%s bytes=%d changed_bytes=4096 total_ms=%.1f sha256=PASS", direction, largeBytes, float64(elapsed.Microseconds())/1000)
			}
		}

	}
}
