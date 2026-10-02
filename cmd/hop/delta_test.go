package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func deltaFixture(t *testing.T) *SFTP {
	t.Helper()
	s := localSFTP(t)
	s.deltaHash = func(ctx context.Context, p string, size, block uint64) ([]byte, error) {
		return exec.CommandContext(ctx, "/bin/sh", "-c", deltaHashScript, "hop", p, fmt.Sprint(size), fmt.Sprint(block)).Output()
	}
	t.Setenv("HOP_TRANSFER_BACKEND", "native")
	return s
}
func TestNativeDeltaRoundTrip(t *testing.T) {
	for _, change := range []string{"edit", "append", "truncate", "shift", "unchanged"} {
		t.Run(change, func(t *testing.T) {
			s := deltaFixture(t)
			if s.ext["copy-data"] != "1" {
				t.Skip("server lacks copy-data")
			}
			dir := t.TempDir()
			source := filepath.Join(dir, "source")
			basis := filepath.Join(dir, "- ' $`\n basis")
			temp := filepath.Join(dir, "upload")
			old := make([]byte, 2<<20)
			for i := range old {
				old[i] = byte((i/65536)*3 + i%251)
			}
			want := append([]byte(nil), old...)
			switch change {
			case "edit":
				copy(want[70000:], []byte("changed data"))
			case "append":
				want = append(want, []byte("tail")...)
			case "truncate":
				want = want[:len(want)-12345]
			case "shift":
				want = append(make([]byte, 65536), want...)
			}
			os.WriteFile(source, want, 0600)
			os.WriteFile(basis, old, 0600)
			f, e := os.Open(source)
			if e != nil {
				t.Fatal(e)
			}
			defer f.Close()
			handled, e := s.deltaUpload(Host{}, f, temp, basis, uint64(len(want)), uint64(len(old)), 0600, nil)
			if e != nil || !handled {
				t.Fatalf("upload handled=%v err=%v", handled, e)
			}
			b, e := os.ReadFile(temp)
			if e != nil || sha256.Sum256(b) != sha256.Sum256(want) {
				t.Fatal("upload differs", e)
			}
			local, e := os.CreateTemp(dir, "download")
			if e != nil {
				t.Fatal(e)
			}
			defer local.Close()
			handled, e = s.deltaDownload(Host{}, temp, basis, local, uint64(len(want)), uint64(len(old)), nil)
			if e != nil || !handled {
				t.Fatalf("download handled=%v err=%v", handled, e)
			}
			b, e = os.ReadFile(local.Name())
			if e != nil || sha256.Sum256(b) != sha256.Sum256(want) {
				t.Fatal("download differs", e)
			}
			if s.deltaTransfers.Load() != 2 || s.deltaReused.Load() < 2<<20 {
				t.Fatal("did not reuse enough bytes")
			}
		})
	}
}
func TestNativeDeltaFallbackAndCorruption(t *testing.T) {
	s := deltaFixture(t)
	dir := t.TempDir()
	basis := filepath.Join(dir, "basis")
	temp := filepath.Join(dir, "temp")
	b := make([]byte, 1<<20)
	os.WriteFile(basis, b, 0600)
	f, _ := os.Open(basis)
	defer f.Close()
	s.deltaHash = func(context.Context, string, uint64, uint64) ([]byte, error) { return nil, errors.New("no shell") }
	handled, e := s.deltaUpload(Host{}, f, temp, basis, uint64(len(b)), uint64(len(b)), 0600, nil)
	if handled || e != nil {
		t.Fatal("missing helper must fall back", handled, e)
	}
	if _, e := os.Stat(temp); !os.IsNotExist(e) {
		t.Fatal("fallback created temp")
	}
	// A corrupt/stale manifest must never produce a committed download.
	s.deltaHash = func(_ context.Context, _ string, size, block uint64) ([]byte, error) {
		sum := fmt.Sprintf("%x", sha256.Sum256(make([]byte, block)))
		return []byte(strings.Repeat(sum+"\n", int(size/block)) + strings.Repeat("0", 64) + "\n"), nil
	}
	out, _ := os.CreateTemp(dir, "download")
	defer out.Close()
	handled, e = s.deltaDownload(Host{}, basis, basis, out, uint64(len(b)), uint64(len(b)), nil)
	if !handled || e == nil || !strings.Contains(e.Error(), "checksum mismatch") {
		t.Fatal("bad manifest accepted", handled, e)
	}
}
func TestNativeHashShellFallback(t *testing.T) {
	s := deltaFixture(t)
	p := filepath.Join(t.TempDir(), "- ' $`\n data")
	b := make([]byte, 131077)
	os.WriteFile(p, b, 0600)
	// Force the POSIX fallback regardless of locally installed Python.
	script := strings.Replace(deltaHashScript, "if command -v python3 >/dev/null 2>&1; then", "if false; then", 1)
	s.deltaHash = func(ctx context.Context, p string, size, block uint64) ([]byte, error) {
		return exec.CommandContext(ctx, "/bin/sh", "-c", script, "hop", p, fmt.Sprint(size), fmt.Sprint(block)).Output()
	}
	for _, block := range []uint64{65536, 0} {
		m, e := s.remoteManifest(Host{}, p, uint64(len(b)), block)
		if e != nil || m.whole != sha256.Sum256(b) {
			t.Fatal("shell checksum", e)
		}
	}
}
func TestRsyncDefaultDisabled(t *testing.T) {
	t.Setenv("HOP_TRANSFER_BACKEND", "")
	if rsyncCandidate(CopyPlan{Files: []PlannedFile{{Size: 8 << 20, Replace: true}}}, false, Options{Overwrite: true}) {
		t.Fatal("default must not use rsync")
	}
}

func TestNativeDeltaFailedVerificationPreservesDestination(t *testing.T) {
	for _, get := range []bool{false, true} {
		t.Run(fmt.Sprint(get), func(t *testing.T) {
			s := deltaFixture(t)
			if !get && s.ext["copy-data"] != "1" {
				t.Skip("copy-data unavailable")
			}
			dir := t.TempDir()
			source := filepath.Join(dir, "source")
			dest := filepath.Join(dir, "dest")
			old := make([]byte, 2<<20)
			want := append([]byte(nil), old...)
			want[70000] = 1
			os.WriteFile(source, want, 0600)
			os.WriteFile(dest, old, 0600)
			runner := s.deltaHash
			s.deltaHash = func(ctx context.Context, p string, size, block uint64) ([]byte, error) {
				out, e := runner(ctx, p, size, block)
				if e == nil && (get || block == 0) {
					lines := strings.Split(strings.TrimSpace(string(out)), "\n")
					lines[len(lines)-1] = strings.Repeat("0", 64)
					out = []byte(strings.Join(lines, "\n") + "\n")
				}
				return out, e
			}
			o := Options{Overwrite: true, SkipReview: true, deferHistory: true, exactDestination: true}
			var e error
			if get {
				e = getFile(s, Host{}, source, dest, o)
			} else {
				e = sendFile(s, Host{}, source, dest, o)
			}
			if e == nil || !strings.Contains(e.Error(), "checksum mismatch") {
				t.Fatal("bad verification accepted", e)
			}
			b, _ := os.ReadFile(dest)
			if sha256.Sum256(b) != sha256.Sum256(old) {
				t.Fatal("destination changed")
			}
			entries, _ := os.ReadDir(dir)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".hop-") {
					t.Fatal("partial file leaked")
				}
			}
		})
	}
}

func TestNativeDeltaEightMiBUpdate(t *testing.T) {
	s := deltaFixture(t)
	if s.ext["copy-data"] != "1" {
		t.Skip("copy-data unavailable")
	}
	dir := t.TempDir()
	basis := filepath.Join(dir, "basis")
	source := filepath.Join(dir, "source")
	dest := filepath.Join(dir, "assembled")
	b := make([]byte, 8<<20)
	for i := range b {
		b[i] = byte((i + i/65536) % 251)
	}
	if e := os.WriteFile(basis, b, 0600); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 4096; i++ {
		b[(4<<20)+i] = 255
	}
	if e := os.WriteFile(source, b, 0600); e != nil {
		t.Fatal(e)
	}
	f, _ := os.Open(source)
	defer f.Close()
	handled, e := s.deltaUpload(Host{}, f, dest, basis, uint64(len(b)), uint64(len(b)), 0600, nil)
	if !handled || e != nil {
		t.Fatal(handled, e)
	}
	reused := s.deltaReused.Load()
	if reused != uint64(len(b))-(64<<10) {
		t.Fatal("unexpected reuse", reused)
	}
	out, _ := os.CreateTemp(dir, "download")
	defer out.Close()
	handled, e = s.deltaDownload(Host{}, dest, basis, out, uint64(len(b)), uint64(len(b)), nil)
	if !handled || e != nil {
		t.Fatal(handled, e)
	}
	if s.deltaReused.Load() != 2*reused {
		t.Fatal("download did not reuse same bytes")
	}
	t.Logf("8 MiB / 4 KiB edit: each direction reuses %d bytes, transfers %d file-data bytes; SHA-256 verified", reused, uint64(len(b))-reused)
}

func TestNativeDeltaUnavailableFallsBack(t *testing.T) {
	s := deltaFixture(t)
	s.deltaHash = func(context.Context, string, uint64, uint64) ([]byte, error) {
		return nil, errors.New("SFTP-only server")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	dest := filepath.Join(dir, "dest")
	download := filepath.Join(dir, "download")
	want := make([]byte, 1<<20)
	want[80000] = 123
	os.WriteFile(source, want, 0600)
	os.WriteFile(dest, make([]byte, len(want)), 0600)
	os.WriteFile(download, make([]byte, len(want)), 0600)
	o := Options{Overwrite: true, SkipReview: true, deferHistory: true, exactDestination: true}
	if e := sendFile(s, Host{}, source, dest, o); e != nil {
		t.Fatal(e)
	}
	if e := getFile(s, Host{}, dest, download, o); e != nil {
		t.Fatal(e)
	}
	actual, _ := os.ReadFile(download)
	if sha256.Sum256(actual) != sha256.Sum256(want) {
		t.Fatal("fallback content differs")
	}
	if s.deltaTransfers.Load() != 0 || s.rsyncCap != nil {
		t.Fatal("unexpected alternate backend")
	}
}

func TestNativeHashRestrictedCommandPaths(t *testing.T) {
	for _, utility := range []string{"sha256sum", "shasum", "openssl"} {
		t.Run(utility, func(t *testing.T) {
			binary, e := exec.LookPath(utility)
			if e != nil {
				t.Skip(utility + " unavailable")
			}
			dd, e := exec.LookPath("dd")
			if e != nil {
				t.Fatal(e)
			}
			bin := t.TempDir()
			if e := os.Symlink(binary, filepath.Join(bin, utility)); e != nil {
				t.Fatal(e)
			}
			if e := os.Symlink(dd, filepath.Join(bin, "dd")); e != nil {
				t.Fatal(e)
			}
			s := deltaFixture(t)
			s.deltaHash = func(ctx context.Context, p string, size, block uint64) ([]byte, error) {
				cmd := exec.CommandContext(ctx, "/bin/sh", "-c", deltaHashScript, "hop", p, fmt.Sprint(size), fmt.Sprint(block))
				cmd.Env = append(os.Environ(), "PATH="+bin)
				return cmd.Output()
			}
			p := filepath.Join(t.TempDir(), "- ' $`\n data")
			b := make([]byte, 131077)
			for i := range b {
				b[i] = byte(i % 251)
			}
			os.WriteFile(p, b, 0600)
			f, _ := os.Open(p)
			defer f.Close()
			want, e := localManifest(f, uint64(len(b)), 65536)
			if e != nil {
				t.Fatal(e)
			}
			got, e := s.remoteManifest(Host{}, p, uint64(len(b)), 65536)
			if e != nil {
				t.Fatal(e)
			}
			if got.whole != want.whole || len(got.blocks) != len(want.blocks) {
				t.Fatal("manifest differs")
			}
			for i := range want.blocks {
				if got.blocks[i] != want.blocks[i] {
					t.Fatal("block differs", i)
				}
			}
		})
	}
}

func TestNativeUploadMissingCopyExtension(t *testing.T) {
	s := deltaFixture(t)
	delete(s.ext, "copy-data")
	s.deltaHash = func(context.Context, string, uint64, uint64) ([]byte, error) {
		t.Fatal("must not probe when copies unavailable")
		return nil, nil
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	dst := filepath.Join(dir, "dest")
	want := make([]byte, 1<<20)
	want[70000] = 123
	os.WriteFile(src, want, 0600)
	os.WriteFile(dst, make([]byte, len(want)), 0600)
	if e := sendFile(s, Host{}, src, dst, Options{Overwrite: true, SkipReview: true, deferHistory: true, exactDestination: true}); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(dst)
	if e != nil || sha256.Sum256(b) != sha256.Sum256(want) {
		t.Fatal("fallback content differs", e)
	}
	if s.deltaTransfers.Load() != 0 {
		t.Fatal("unexpected delta")
	}
}
