package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"hash/adler32"
	"os"
	"path/filepath"
	"testing"
)

func TestRollingAdlerMatchesStandard(t *testing.T) {
	b := make([]byte, 140000)
	rand.Read(b)
	for _, n := range []int{1, 64, 65536, 70000} {
		sum := adler32.Checksum(b[:n])
		for off := 1; off < 500; off++ {
			sum = rollAdler(sum, b[off-1], b[off+n-1], uint64(n))
			if sum != adler32.Checksum(b[off:off+n]) {
				t.Fatal("rolling checksum differs", n, off)
			}
		}
	}
}
func TestRollingRejectsWeakCollision(t *testing.T) {
	b := make([]byte, 65536)
	for i := range b {
		b[i] = 100
	}
	other := append([]byte(nil), b...)
	other[10]++
	other[11] -= 2
	other[12]++
	if adler32.Checksum(b) != adler32.Checksum(other) || sha256.Sum256(b) == sha256.Sum256(other) {
		t.Fatal("bad collision fixture")
	}
	p := filepath.Join(t.TempDir(), "file")
	os.WriteFile(p, other, 0600)
	f, _ := os.Open(p)
	defer f.Close()
	m := deltaManifest{blocks: [][32]byte{sha256.Sum256(b)}, weak: []uint32{adler32.Checksum(b)}}
	if e := scanDeltaMatches(context.Background(), f, 65536, 65536, 65536, m, func(uint64, int) { t.Fatal("accepted weak collision") }); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := scanDeltaMatches(ctx, f, 65536, 65536, 65536, m, func(uint64, int) {}); e != context.Canceled {
		t.Fatal("did not cancel", e)
	}
}
func TestNativeRollingInsertDelete(t *testing.T) {
	for _, kind := range []string{"insert-one", "insert-4096", "delete-one", "delete-4096"} {
		t.Run(kind, func(t *testing.T) {
			s := deltaFixture(t)
			if s.ext["copy-data"] != "1" {
				t.Skip("copy-data unavailable")
			}
			b := make([]byte, 2<<20)
			rand.Read(b)
			at := len(b) / 4
			n := 1
			if kind == "insert-4096" || kind == "delete-4096" {
				n = 4096
			}
			want := append([]byte(nil), b[:at]...)
			if kind == "insert-one" || kind == "insert-4096" {
				want = append(want, make([]byte, n)...)
				want = append(want, b[at:]...)
			} else {
				want = append(want, b[at+n:]...)
			}
			dir := t.TempDir()
			basis := filepath.Join(dir, "basis")
			src := filepath.Join(dir, "source")
			dst := filepath.Join(dir, "assembled")
			os.WriteFile(basis, b, 0600)
			os.WriteFile(src, want, 0600)
			f, _ := os.Open(src)
			defer f.Close()
			handled, e := s.deltaUpload(Host{}, f, dst, basis, uint64(len(want)), uint64(len(b)), 0600, nil)
			if e != nil || !handled {
				t.Fatal("upload", handled, e)
			}
			got, _ := os.ReadFile(dst)
			if sha256.Sum256(got) != sha256.Sum256(want) {
				t.Fatal("upload differs")
			}
			reused := s.deltaReused.Load()
			if reused < uint64(len(want))-(2<<16) {
				t.Fatal("insufficient upload reuse", reused)
			}
			out, _ := os.CreateTemp(dir, "download")
			defer out.Close()
			handled, e = s.deltaDownload(Host{}, dst, basis, out, uint64(len(want)), uint64(len(b)), nil)
			if e != nil || !handled {
				t.Fatal("download", handled, e)
			}
			got, _ = os.ReadFile(out.Name())
			if sha256.Sum256(got) != sha256.Sum256(want) {
				t.Fatal("download differs")
			}
			if s.deltaReused.Load()-reused < uint64(len(want))-(2<<16) {
				t.Fatal("insufficient download reuse")
			}
		})
	}
}

func TestRollingCancellationAfterUnalignedMatch(t *testing.T) {
	const block = 65536
	original := make([]byte, 3*block)
	rand.Read(original)
	m := deltaManifest{}
	for i := 0; i < 3; i++ {
		b := original[i*block : (i+1)*block]
		m.blocks = append(m.blocks, sha256.Sum256(b))
		m.weak = append(m.weak, adler32.Checksum(b))
	}
	p := filepath.Join(t.TempDir(), "file")
	os.WriteFile(p, append([]byte{123}, original...), 0600)
	f, _ := os.Open(p)
	defer f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	matches := 0
	e := scanDeltaMatches(ctx, f, uint64(len(original)+1), uint64(len(original)), block, m, func(uint64, int) { matches++; cancel() })
	if e != context.Canceled || matches != 1 {
		t.Fatal("unaligned scan did not stop promptly", e, matches)
	}
}
