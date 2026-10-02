package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in real SSH comparison. Only generated files in a unique remote /tmp
// directory are touched; resets and external verification are outside timing.
func TestLiveTransferMatrix(t *testing.T) {
	target := os.Getenv("HOP_MATRIX_HOST")
	if target == "" {
		t.Skip("set HOP_MATRIX_HOST for authorized live comparison")
	}
	if !validTarget(target) {
		t.Fatal("invalid host")
	}
	ssh := func(command string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		b, e := exec.CommandContext(ctx, "/usr/bin/ssh", "-o", "BatchMode=yes", "--", target, command).CombinedOutput()
		if e != nil {
			t.Fatalf("fixture command: %v: %s", e, b)
		}
		return b
	}
	remote := strings.TrimSpace(string(ssh("mktemp -d /tmp/hop-matrix.XXXXXXXXXXXX")))
	if !strings.HasPrefix(remote, "/tmp/hop-matrix.") || strings.ContainsAny(strings.TrimPrefix(remote, "/tmp/"), "/'\" \n\r\t;$`\\") {
		t.Fatal("unexpected fixture path")
	}
	t.Cleanup(func() {
		ssh("rm -rf -- " + shellQuote(remote) + " && test ! -e " + shellQuote(remote))
		t.Log("REMOTE_CLEANUP_OK")
	})
	previous := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = previous }()
	host := Host{Target: target, Options: []string{"-o", "BatchMode=yes"}}
	connections := map[string]*SFTP{}
	for _, backend := range []string{"sftp", "native", "rsync"} {
		s, e := connectSFTP(host)
		if e != nil {
			t.Fatal(e)
		}
		defer s.Close()
		connections[backend] = s
	}
	seed := make([]byte, 2<<20)
	if _, e := rand.Read(seed); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	seedPath := filepath.Join(root, "seed")
	if e := os.WriteFile(seedPath, seed, 0600); e != nil {
		t.Fatal(e)
	}
	f, _ := os.Open(seedPath)
	e := connections["sftp"].Upload(f, remote+"/seed", uint64(len(seed)), 0600, nil)
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	variants := map[string][]byte{}
	variants["edit"] = append([]byte(nil), seed...)
	copy(variants["edit"][len(seed)/2:], []byte(strings.Repeat("X", 4096)))
	variants["append"] = append(append([]byte(nil), seed...), []byte(strings.Repeat("X", 4096))...)
	variants["insert"] = append(append([]byte(nil), seed[:len(seed)/4]...), []byte(strings.Repeat("X", 4096))...)
	variants["insert"] = append(variants["insert"], seed[len(seed)/4:]...)
	variants["fresh"] = seed
	variants["replace"] = make([]byte, len(seed))
	if _, e := rand.Read(variants["replace"]); e != nil {
		t.Fatal(e)
	}
	// Replacement bytes need one additional seed upload; all other variants
	// are derived on the server, so fixture setup does not dominate the run.
	replacement := filepath.Join(root, "replacement")
	os.WriteFile(replacement, variants["replace"], 0600)
	f, _ = os.Open(replacement)
	e = connections["sftp"].Upload(f, remote+"/replacement", uint64(len(seed)), 0600, nil)
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	script := `import pathlib,sys
p=pathlib.Path(sys.argv[1]); b=(p/'seed').read_bytes(); x=b'X'*4096
v={'edit':b[:len(b)//2]+x+b[len(b)//2+4096:],'append':b+x,'insert':b[:len(b)//4]+x+b[len(b)//4:],'fresh':b,'replace':(p/'replacement').read_bytes()}
for name,data in v.items():
 d=p/name;d.mkdir();(d/'file').write_bytes(data)
(p/'target').mkdir()
`
	ssh("python3 -c " + shellQuote(script) + " " + shellQuote(remote))
	for _, kind := range []string{"edit", "append", "insert", "fresh", "replace"} {
		source := filepath.Join(root, kind)
		os.Mkdir(source, 0700)
		os.WriteFile(filepath.Join(source, "file"), variants[kind], 0600)
	}
	type result struct {
		Kind, Backend, Direction     string
		Repeat                       int
		Bytes                        int
		Milliseconds                 float64
		NativeTransfers, ReusedBytes uint64
		SHA256                       string
	}
	for repeat := 0; repeat < 3; repeat++ {
		for _, kind := range []string{"edit", "append", "insert", "fresh", "replace"} {
			backends := []string{"native", "sftp", "rsync"}
			if selected := os.Getenv("HOP_MATRIX_BACKEND"); selected != "" {
				if selected != "native" && selected != "sftp" && selected != "rsync" {
					t.Fatal("invalid backend")
				}
				backends = []string{selected}
			}
			for j := 0; j < len(backends); j++ {
				backend := backends[(j+repeat)%len(backends)]
				s := connections[backend]
				t.Setenv("HOP_TRANSFER_BACKEND", backend)
				for _, get := range []bool{false, true} {
					dir := filepath.Join(root, "target")
					os.MkdirAll(dir, 0700)
					local := filepath.Join(root, kind, "file")
					src := local
					dst := remote + "/target"
					direction := "upload"
					if get {
						src = remote + "/" + kind + "/file"
						dst = dir
						direction = "download"
						local = filepath.Join(dir, "file")
					}
					if get {
						if kind == "fresh" {
							os.Remove(local)
						} else {
							os.WriteFile(local, seed, 0600)
						}
					} else {
						if kind == "fresh" {
							ssh("rm -f -- " + shellQuote(remote+"/target/file"))
						} else {
							ssh("cp " + shellQuote(remote+"/seed") + " " + shellQuote(remote+"/target/file"))
						}
					}
					beforeCount, beforeBytes := s.deltaTransfers.Load(), s.deltaReused.Load()
					started := time.Now()
					plan, e := planBatch(s, []FileItem{{Path: src, Regular: true}}, dst, get, kind != "fresh")
					if e != nil {
						t.Fatal(e)
					}
					p := &batchProgress{embedded: true, total: plan.Total, files: 1, started: time.Now()}
					if e := executeBatchPlan(s, host, plan, get, Options{Overwrite: kind != "fresh"}, p); e != nil {
						t.Fatalf("%s %s %s: %v", kind, backend, direction, e)
					}
					elapsed := time.Since(started)
					want := fmt.Sprintf("%x", sha256.Sum256(variants[kind]))
					var actual string
					if get {
						b, e := os.ReadFile(local)
						if e != nil {
							t.Fatal(e)
						}
						actual = fmt.Sprintf("%x", sha256.Sum256(b))
					} else {
						actual = strings.Fields(string(ssh("sha256sum < " + shellQuote(remote+"/target/file"))))[0]
					}
					if actual != want {
						t.Fatal("checksum mismatch", kind, backend, direction)
					}
					if backend == "native" && s.rsyncCap != nil {
						t.Fatal("native probed rsync")
					}
					if backend == "rsync" && s.rsyncCap == nil {
						t.Fatal("rsync comparator silently fell back")
					}
					r := result{kind, backend, direction, repeat + 1, len(variants[kind]), float64(elapsed.Microseconds()) / 1000, s.deltaTransfers.Load() - beforeCount, s.deltaReused.Load() - beforeBytes, "PASS"}
					b, _ := json.Marshal(r)
					t.Log("MATRIX " + string(b))
				}
			}
		}
	}
}

// Exercises actual remote POSIX utilities in an isolated PATH. Disabling an
// advertised extension models an older server; it is not a second server OS.
func TestLivePortableTransfer(t *testing.T) {
	target := os.Getenv("HOP_MATRIX_HOST")
	if target == "" {
		t.Skip("set HOP_MATRIX_HOST")
	}
	if !validTarget(target) {
		t.Fatal("invalid host")
	}
	t.Setenv("HOP_TRANSFER_BACKEND", "native")
	ssh := func(command string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		return exec.CommandContext(ctx, "/usr/bin/ssh", "-o", "BatchMode=yes", "--", target, command).CombinedOutput()
	}
	b, e := ssh("mktemp -d /tmp/hop-portable.XXXXXXXXXXXX")
	if e != nil {
		t.Fatal(e, string(b))
	}
	remote := strings.TrimSpace(string(b))
	if !strings.HasPrefix(remote, "/tmp/hop-portable.") || strings.ContainsAny(strings.TrimPrefix(remote, "/tmp/"), "/'\" \n\r\t;$`\\") {
		t.Fatal("unexpected fixture path")
	}
	t.Cleanup(func() {
		if b, e := ssh("rm -rf -- " + shellQuote(remote) + " && test ! -e " + shellQuote(remote)); e != nil {
			t.Error(e, string(b))
		} else {
			t.Log("REMOTE_CLEANUP_OK")
		}
	})
	host := Host{Target: target, Options: []string{"-o", "BatchMode=yes"}}
	s, e := connectSFTP(host)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if b, e := ssh("mkdir " + shellQuote(remote+"/bin") + " " + shellQuote(remote+"/empty") + " && ln -s /usr/bin/dd " + shellQuote(remote+"/bin/dd") + " && ln -s /usr/bin/sha256sum " + shellQuote(remote+"/bin/sha256sum")); e != nil {
		t.Fatal(e, string(b))
	}
	dir := t.TempDir()
	seed := make([]byte, 1<<20)
	rand.Read(seed)
	seedPath := filepath.Join(dir, "seed")
	os.WriteFile(seedPath, seed, 0600)
	f, _ := os.Open(seedPath)
	e = s.Upload(f, remote+"/seed", uint64(len(seed)), 0600, nil)
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	want := append([]byte(nil), seed...)
	copy(want[len(want)/2:], []byte(strings.Repeat("X", 4096)))
	src := filepath.Join(dir, "source")
	os.WriteFile(src, want, 0600)
	originalCopy := s.ext["copy-data"]
	for _, capability := range []string{"posix-only", "no-hash-tools", "no-copy-data"} {
		bin := remote + "/bin"
		if capability == "no-hash-tools" {
			bin = remote + "/empty"
		}
		s.ext["copy-data"] = originalCopy
		if capability == "no-copy-data" {
			delete(s.ext, "copy-data")
		}
		s.deltaHash = func(ctx context.Context, p string, size, block uint64) ([]byte, error) {
			command := "PATH=" + shellQuote(bin) + " /bin/sh -c " + shellQuote(deltaHashScript) + " hop " + shellQuote(p) + " " + fmt.Sprint(size) + " " + fmt.Sprint(block)
			return exec.CommandContext(ctx, "/usr/bin/ssh", "-o", "BatchMode=yes", "--", target, command).Output()
		}
		if b, e := ssh("cp " + shellQuote(remote+"/seed") + " " + shellQuote(remote+"/file")); e != nil {
			t.Fatal(e, string(b))
		}
		dst := filepath.Join(dir, "download")
		os.WriteFile(dst, seed, 0600)
		for _, get := range []bool{false, true} {
			count := s.deltaTransfers.Load()
			start := time.Now()
			o := Options{Overwrite: true, SkipReview: true, deferHistory: true, exactDestination: true}
			direction := "upload"
			if get {
				direction = "download"
				e = getFile(s, host, remote+"/file", dst, o)
			} else {
				e = sendFile(s, host, src, remote+"/file", o)
			}
			if e != nil {
				t.Fatal(capability, direction, e)
			}
			elapsed := time.Since(start)
			expectedDelta := capability == "posix-only" || capability == "no-copy-data" && get
			actualDelta := s.deltaTransfers.Load() == count+1
			if actualDelta != expectedDelta {
				t.Fatal("unexpected backend", capability, direction, actualDelta)
			}
			expected := fmt.Sprintf("%x", sha256.Sum256(want))
			var actual string
			if get {
				b, e := os.ReadFile(dst)
				if e != nil {
					t.Fatal(e)
				}
				actual = fmt.Sprintf("%x", sha256.Sum256(b))
			} else {
				b, e := ssh("sha256sum < " + shellQuote(remote+"/file"))
				if e != nil {
					t.Fatal(e)
				}
				actual = strings.Fields(string(b))[0]
			}
			if actual != expected {
				t.Fatal("checksum differs")
			}
			t.Logf("PORTABLE capability=%s direction=%s native=%v milliseconds=%.1f sha256=PASS", capability, direction, actualDelta, float64(elapsed.Microseconds())/1000)
		}
	}
}
