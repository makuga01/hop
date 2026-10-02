package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Optional repeatable comparison against a local OpenSSH server, with a fixed
// response delay. Delaying a stream by a constant time models latency without
// turning each packet into an artificially serialized sleep.
// HOP_PERF=1 go test ./cmd/hop -run '^TestTransferPerformance$' -v -count=1
func TestTransferPerformance(t *testing.T) {
	if os.Getenv("HOP_PERF") != "1" {
		t.Skip("set HOP_PERF=1 to run transfer measurements")
	}
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	counts := []int{1, 64}
	largeSize := 32 << 20
	if os.Getenv("HOP_PERF_SCALE") == "1" {
		counts = []int{1, 1024}
		largeSize = 256 << 20
	}
	for _, delay := range []time.Duration{0, 20 * time.Millisecond} {
		for _, count := range counts {
			for _, get := range []bool{false, true} {
				direction := "upload"
				if get {
					direction = "download"
				}
				t.Run(fmt.Sprintf("%s/files=%d/rtt=%s", direction, count, delay), func(t *testing.T) {
					s := localSFTP(t)
					if delay != 0 {
						delaySFTPReplies(t, s, delay)
					}
					source, dest := t.TempDir(), t.TempDir()
					size := 4096
					if count == 1 {
						size = largeSize
					}
					payload := make([]byte, size)
					for i := range payload {
						payload[i] = byte((i*31 + i/256) % 251)
					}
					var roots []FileItem
					for i := 0; i < count; i++ {
						name := filepath.Join(source, fmt.Sprintf("file-%03d", i))
						if err := os.WriteFile(name, payload, 0600); err != nil {
							t.Fatal(err)
						}
						roots = append(roots, FileItem{Path: name, Regular: true})
					}
					started := time.Now()
					plan, err := planBatch(s, roots, dest, get, false)
					if err != nil {
						t.Fatal(err)
					}
					scan := time.Since(started)
					p := &batchProgress{embedded: true, total: plan.Total, files: count, started: time.Now()}
					err = executeBatchPlan(s, Host{Target: "benchmark-fixture"}, plan, get, Options{}, p)
					elapsed := time.Since(started)
					if err != nil {
						t.Fatal(err)
					}
					expected := sha256.Sum256(payload)
					for _, f := range roots {
						b, err := os.ReadFile(filepath.Join(dest, filepath.Base(f.Path)))
						if err != nil || sha256.Sum256(b) != expected {
							t.Fatalf("content mismatch: %s: %v", f.Path, err)
						}
					}
					t.Logf("RESULT files=%d bytes=%d scan_ms=%.1f total_ms=%.1f MiB_s=%.2f", count, plan.Total, float64(scan.Microseconds())/1000, float64(elapsed.Microseconds())/1000, float64(plan.Total)/(1<<20)/elapsed.Seconds())
				})
			}
		}
	}
}

func delaySFTPReplies(t *testing.T, s *SFTP, delay time.Duration) {
	t.Helper()
	original := s.out
	r, w := io.Pipe()
	s.out = r
	type packet struct {
		b  []byte
		at time.Time
	}
	packets := make(chan packet, 1024)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop); r.Close(); w.Close() })
	go func() {
		defer close(packets)
		server := &SFTP{out: original}
		for {
			typ, b, err := server.receive()
			if err != nil {
				return
			}
			data := fields(u32(uint32(len(b)+1)), []byte{typ}, b)
			select {
			case packets <- packet{data, time.Now().Add(delay)}:
			case <-stop:
				return
			}
		}
	}()
	go func() {
		defer w.Close()
		for p := range packets {
			timer := time.NewTimer(max(0, time.Until(p.at)))
			select {
			case <-timer.C:
			case <-stop:
				timer.Stop()
				return
			}
			if _, err := io.Copy(w, bytes.NewReader(p.b)); err != nil {
				return
			}
		}
	}()
}
