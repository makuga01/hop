package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Existing images only; never pulls images or installs packages.
func TestNativeHashLinuxContainers(t *testing.T) {
	if os.Getenv("HOP_COMPAT_DOCKER") != "1" {
		t.Skip("set HOP_COMPAT_DOCKER=1 with existing alpine and ubuntu images")
	}
	for _, image := range []string{"alpine:latest", "ubuntu:22.04"} {
		t.Run(image, func(t *testing.T) {
			dir := t.TempDir()
			name := "- ' $` data"
			p := filepath.Join(dir, name)
			b := make([]byte, 131077)
			for i := range b {
				b[i] = byte(i % 251)
			}
			os.WriteFile(p, b, 0644)
			s := deltaFixture(t)
			s.deltaHash = func(ctx context.Context, _ string, size, block uint64) ([]byte, error) {
				return exec.CommandContext(ctx, "docker", "run", "--rm", "--pull=never", "--network=none", "--read-only", "--mount", "type=bind,src="+dir+",dst=/fixture,readonly", image, "/bin/sh", "-c", deltaHashScript, "hop", "/fixture/"+name, fmt.Sprint(size), fmt.Sprint(block)).Output()
			}
			for _, block := range []uint64{65536, 0} {
				m, e := s.remoteManifest(Host{}, p, uint64(len(b)), block)
				if e != nil {
					t.Fatal(e)
				}
				if m.whole != sha256.Sum256(b) {
					t.Fatal("whole checksum differs")
				}
				if block > 0 {
					for i, sum := range m.blocks {
						start := i * int(block)
						end := min(start+int(block), len(b))
						if sum != sha256.Sum256(b[start:end]) {
							t.Fatal("block differs")
						}
					}
				}
			}
		})
	}
}
