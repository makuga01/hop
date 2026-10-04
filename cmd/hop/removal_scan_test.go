package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func compareRemovalPlans(t *testing.T, a, b []removalEntry) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("different entry counts: %d / %d", len(a), len(b))
	}
	want := map[string]removalEntry{}
	for _, e := range a {
		want[e.name] = e
	}
	positions := map[string]int{}
	for i, e := range b {
		original, ok := want[e.name]
		if !ok || original.attr.Mode != e.attr.Mode || original.attr.Size != e.attr.Size || original.attr.Mtime != e.attr.Mtime || strings.Join(original.children, "\x00") != strings.Join(e.children, "\x00") {
			t.Fatal("different removal entry", e.name)
		}
		positions[e.name] = i
	}
	for i, e := range b {
		for _, child := range e.children {
			if childIndex, ok := positions[path.Join(e.name, child)]; !ok || childIndex >= i {
				t.Fatal("not a complete postorder plan")
			}
		}
	}
}

func TestRemovalScanHelper(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3")
	}
	s := localSFTP(t)
	root := t.TempDir()
	tree := filepath.Join(root, "tree")
	os.Mkdir(tree, 0700)
	for i := 0; i < 300; i++ {
		dir := filepath.Join(tree, fmt.Sprint(i%10))
		os.MkdirAll(dir, 0700)
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("file %d\nλ", i)), []byte("fixture"), 0600)
	}
	outside := filepath.Join(root, "outside")
	os.Mkdir(outside, 0700)
	os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0600)
	os.Symlink(outside, filepath.Join(tree, "link"))
	os.Symlink("missing", filepath.Join(tree, "broken"))
	os.Mkdir(filepath.Join(tree, "empty"), 0700)
	roots := []FileItem{{Path: filepath.Join(tree, "0"), Dir: true}, {Path: tree, Dir: true}, {Path: tree, Dir: true}}
	before, err := planRemoval(context.Background(), s, roots, true)
	if err != nil {
		t.Fatal(err)
	}
	s.removalScanCommand = func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, python, "-c", removalScanPython) }
	progress := newScanProgress()
	after, err := planRemovalObserved(context.Background(), s, roots, true, progress)
	if err != nil {
		t.Fatal(err)
	}
	compareRemovalPlans(t, before, after)
	if progress.files != 302 || progress.dirs != 12 {
		t.Fatal("incorrect scan progress", progress.files, progress.dirs)
	}
	for _, entry := range after {
		if strings.Contains(entry.name, "link/keep") {
			t.Fatal("scan followed symlink")
		}
	}
	// The read-only helper's plan still goes through existing removal safeguards.
	if err := executeRemoval(context.Background(), s, after, true, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(outside, "keep")); err != nil || string(got) != "keep" {
		t.Fatal("symlink target changed")
	}
}

func TestRemovalScanFallbackAndChanges(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3")
	}
	for _, kind := range []string{"fallback", "namespace", "changed", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			s := localSFTP(t)
			tree := t.TempDir()
			file := filepath.Join(tree, "keep")
			os.WriteFile(file, []byte("original"), 0600)
			script := removalScanPython
			if kind == "fallback" {
				script = "raise SystemExit(1)"
			}
			if kind == "namespace" {
				script = strings.Replace(script, "if os.path.realpath(os.path.dirname(p))!=base64.b64decode(canonical,validate=True):", "if True:", 1)
			}
			s.removalScanCommand = func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, python, "-c", script) }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancel" {
				cancel()
			}
			entries, err := planRemovalObserved(ctx, s, []FileItem{{Path: tree, Dir: true}}, true, newScanProgress())
			if kind == "cancel" {
				if err == nil {
					t.Fatal("canceled scan succeeded")
				}
				return
			}
			if err != nil || len(entries) != 2 {
				t.Fatal("scan/fallback failed", err)
			}
			if kind == "changed" {
				os.WriteFile(file, []byte("changed and longer"), 0600)
				if err := executeRemoval(ctx, s, entries, true, nil); err == nil {
					t.Fatal("changed source deleted")
				}
			}
			if _, err := os.Stat(file); err != nil {
				t.Fatal("read-only scan changed source")
			}
		})
	}
}

func TestRemovalScanRejectsMalformedTree(t *testing.T) {
	name := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	row := func(parent int, s string, mode uint32) string {
		b, _ := json.Marshal([]any{parent, name(s), mode, 0, 0})
		return string(b) + "\n"
	}
	root := row(-1, "/fixture", 0040700)
	valid := root + row(0, "child", 0100600) + "END 2\n"
	if _, err := readRemovalScan(strings.NewReader("HOPRM1\n"+valid), []string{"/fixture"}, []Attr{{Mode: 0040700}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		root + row(0, "../escape", 0100600) + "END 2\n",
		root + row(0, "child", 0120700) + row(1, "escape", 0100600) + "END 3\n",
		root + row(9, "child", 0100600) + "END 2\n",
		root + row(0, "child", 0100600) + row(0, "child", 0100600) + "END 3\n",
		row(-1, "/other", 0040700) + "END 1\n",
		root + "END 1\n" + row(0, "extra", 0100600), root, "END 0\n",
	} {
		if _, err := readRemovalScan(strings.NewReader("HOPRM1\n"+body), []string{"/fixture"}, []Attr{{Mode: 0040700}}, nil); err == nil {
			t.Fatal("unsafe manifest accepted")
		}
	}
}

// Benchmark the former per-entry stat walk, only on generated test trees.
func legacyRemovalFixtureWalk(s *SFTP, name string) ([]removalEntry, error) {
	r, err := removalStat(s, name, true)
	if err != nil {
		return nil, err
	}
	entries := []removalEntry{}
	if r.attr.Dir() {
		r.children, err = removalChildren(s, name, true)
		if err != nil {
			return nil, err
		}
		for _, child := range r.children {
			part, err := legacyRemovalFixtureWalk(s, path.Join(name, child))
			if err != nil {
				return nil, err
			}
			entries = append(entries, part...)
		}
	}
	return append(entries, r), nil
}

func TestLiveRemovalScan(t *testing.T) {
	host := os.Getenv("HOP_REMOVAL_HOST")
	if host == "" {
		t.Skip("set HOP_REMOVAL_HOST for an authorized scan benchmark")
	}
	if !validTarget(host) {
		t.Fatal("invalid host")
	}
	ssh := func(command string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		b, err := exec.CommandContext(ctx, "/usr/bin/ssh", "-o", "BatchMode=yes", "--", host, command).CombinedOutput()
		if err != nil {
			t.Fatal(err, string(b))
		}
		return b
	}
	root := strings.TrimSpace(string(ssh("mktemp -d /tmp/hop-removal.XXXXXXXXXXXX")))
	if !strings.HasPrefix(root, "/tmp/hop-removal.") || strings.ContainsAny(strings.TrimPrefix(root, "/tmp/"), "/'\" \n\r\t;$`\\") {
		t.Fatal("invalid fixture directory")
	}
	defer func() {
		ssh("rm -rf -- " + shellQuote(root) + " && test ! -e " + shellQuote(root))
		t.Log("REMOTE_CLEANUP_OK")
	}()
	count := 512
	if value := os.Getenv("HOP_REMOVAL_COUNT"); value != "" {
		var err error
		count, err = strconv.Atoi(value)
		if err != nil || count < 1 || count > 50000 {
			t.Fatal("invalid count")
		}
	}
	dirs := min(count, 16)
	if value := os.Getenv("HOP_REMOVAL_DIRS"); value != "" {
		var err error
		dirs, err = strconv.Atoi(value)
		if err != nil || dirs < 1 || dirs > count || dirs > 2000 {
			t.Fatal("invalid directory count")
		}
	}
	script := fmt.Sprintf("import os\nr=%s\nfor i in range(%d):\n d=os.path.join(r,str(i%%%d));os.makedirs(d,exist_ok=True)\n with open(os.path.join(d,str(i)),'wb') as f:f.write(b'fixture')\nos.symlink('/does-not-exist',os.path.join(r,'broken'))\n", strconv.Quote(root), count, dirs)
	ssh("python3 -c " + shellQuote(script))
	s, err := connectSFTP(Host{Target: host, Options: []string{"-o", "BatchMode=yes"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var before []removalEntry
	if os.Getenv("HOP_REMOVAL_BASELINE") != "0" {
		start := time.Now()
		before, err = legacyRemovalFixtureWalk(s, root)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("REMOVAL_SCAN legacy entries=%d seconds=%.3f", len(before), time.Since(start).Seconds())
	}
	progress := newScanProgress()
	start := time.Now()
	after, err := s.scanRemovalWithHelper(context.Background(), []FileItem{{Path: root, Dir: true}}, progress)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("REMOVAL_SCAN helper entries=%d seconds=%.3f files=%d dirs=%d", len(after), time.Since(start).Seconds(), progress.files, progress.dirs)
	if len(after) != count+dirs+2 {
		t.Fatal("incomplete plan")
	}
	if before != nil {
		compareRemovalPlans(t, before, after)
	}
	// Read-only scan: the entire generated selection must still be present.
	got := strings.TrimSpace(string(ssh("python3 -c " + shellQuote("import os;print(sum(len(f) for _,_,f in os.walk("+strconv.Quote(root)+")))"))))
	if got != strconv.Itoa(count+1) {
		t.Fatal("scan changed fixture")
	}
}

func TestRemovalScanPanelShowsCounts(t *testing.T) {
	scan := newScanProgress()
	scan.setPhase("Scanning selection for deletion", 0)
	scan.found(false)
	scan.found(true)
	d := &dualManager{transfer: &panelTransfer{stage: "scan", scan: scan, action: managerAction{kind: "delete"}}}
	lines := strings.Join(d.transferLines(100, 7), "\n")
	if !strings.Contains(lines, "1 files · 1 folders found") {
		t.Fatal(lines)
	}
}

func TestRemovalSFTPReusesListingAttributes(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			stats, reads := 0, 0
			attrs := func(mode uint32) []byte { return fields(u32(13), u64(0), u32(mode), u32(0), u32(0)) }
			s := protocolFixture(t, func(typ byte, b []byte) (byte, []byte) {
				d := decoder{b: b}
				id := d.u32()
				switch typ {
				case fxLstat:
					stats++
					name := d.str()
					mode := uint32(0100600)
					if name == "/fixture" {
						mode = 0040700
					}
					return fxAttrs, fields(u32(id), attrs(mode))
				case fxOpendir:
					return fxHandle, fields(u32(id), str("directory"))
				case fxReaddir:
					reads++
					if reads > 1 {
						return fxStatus, fields(u32(id), u32(fxEOF), str("end"), str(""))
					}
					out := fields(u32(id), u32(32))
					for i := 0; i < 32; i++ {
						a := u32(0)
						if complete {
							a = attrs(0100600)
						}
						out = append(out, fields(str(fmt.Sprint(i)), str(""), a)...)
					}
					return fxName, out
				case fxClose:
					return fxStatus, fields(u32(id), u32(0), str(""), str(""))
				default:
					t.Errorf("unexpected request %d", typ)
					return fxStatus, fields(u32(id), u32(4), str("unexpected"), str(""))
				}
			})
			entries, err := planRemoval(context.Background(), s, []FileItem{{Path: "/fixture", Dir: true}}, true)
			want := 33
			if complete {
				want = 1
			}
			if err != nil || len(entries) != 33 || stats != want {
				t.Fatal("incorrect metadata reuse", err, stats, want)
			}
		})
	}
}
