package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func pythonScan(t *testing.T, s *SFTP) {
	t.Helper()
	binary, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	s.scanCommand = func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, binary, "-c", scanPython) }
}

func TestScanHelperMatchesSFTP(t *testing.T) {
	s := localSFTP(t)
	root := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(filepath.Join(root, "real", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"regular", "space name", "quote'\";$(false)", "new\nline", string([]byte{'b', 0xff})} {
		if err := os.WriteFile(filepath.Join(root, "real", name), []byte("contents"), 0600); err != nil {
			if errors.Is(err, syscall.EILSEQ) {
				continue
			} // Some local filesystems require UTF-8.
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	plain, err := planBatch(s, []FileItem{{Path: root}}, dest, true, false)
	if err != nil {
		t.Fatal(err)
	}
	pythonScan(t, s)
	progress := newScanProgress()
	fast, err := planBatchObserved(s, []FileItem{{Path: root}}, dest, true, false, progress)
	if err != nil {
		t.Fatal(err)
	}
	asMap := func(p CopyPlan) map[string]PlannedFile {
		m := map[string]PlannedFile{}
		for _, f := range p.Files {
			m[f.Remote] = f
		}
		return m
	}
	if fast.Total != plain.Total || !reflect.DeepEqual(asMap(fast), asMap(plain)) || len(fast.Directories) != len(plain.Directories) {
		t.Fatal("helper changed copy plan")
	}
	if progress.files != len(fast.Files) || progress.checked != len(fast.Files) {
		t.Fatal("inaccurate progress")
	}
	a, err := s.Stat(root, true)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.scanWithHelper([]FileItem{{Path: root, Dir: true}}, map[string]Attr{root: a}, nil)
	if err != nil || !result.readChecked {
		t.Fatal("helper unexpectedly fell back", err)
	}
	// A loop must fail before any destination is created, through either path.
	if err := os.Symlink(root, filepath.Join(root, "real", "loop")); err != nil {
		t.Fatal(err)
	}
	if _, err = planBatch(s, []FileItem{{Path: root}}, dest, true, false); err == nil || !strings.Contains(err.Error(), "symlink loop") {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 0 {
		t.Fatal("scan wrote files")
	}
}

func TestScanHelperUnavailableFallback(t *testing.T) {
	s := localSFTP(t)
	source := t.TempDir()
	os.WriteFile(filepath.Join(source, "file"), []byte("hello"), 0600)
	s.scanCommand = func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "printf garbage; exit 127")
	}
	p, err := planBatch(s, []FileItem{{Path: source}}, t.TempDir(), true, false)
	if err != nil || len(p.Files) != 1 || p.Total != 5 {
		t.Fatal("fallback failed", err)
	}
}

func TestScanManifestRejectsMalformedTrees(t *testing.T) {
	enc := func(p string) string { return base64.StdEncoding.EncodeToString([]byte(p)) }
	root := scanRecord{Path: enc("/tree"), Real: enc("/tree"), Mode: 0040700}
	file := scanRecord{Path: enc("/tree/file"), Mode: 0100600, Size: 1}
	outside := file
	outside.Path = enc("/elsewhere/file")
	traversal := file
	traversal.Path = enc("/tree/../file")
	missingParent := file
	missingParent.Path = enc("/tree/missing/file")
	cycle := root
	cycle.Path = enc("/tree/loop")
	zero, negative, future, one := 0, -1, 3, 1
	compact := file
	compact.Path = enc("file")
	compact.Parent = &zero
	badParent := compact
	badParent.Parent = &negative
	forwardParent := compact
	forwardParent.Parent = &future
	fileParent := compact
	fileParent.Parent = &one
	escape := compact
	escape.Path = enc("../outside")
	absolute := compact
	absolute.Path = enc("/elsewhere")
	cases := map[string][]scanRecord{
		"negative parent":    {root, badParent, {Done: true}},
		"forward parent":     {root, forwardParent, {Done: true}},
		"file parent":        {root, file, fileParent, {Done: true}},
		"relative escape":    {root, escape, {Done: true}},
		"absolute child":     {root, absolute, {Done: true}},
		"missing completion": {root, file}, "duplicate": {root, file, file, {Done: true}},
		"outside": {root, outside, {Done: true}}, "traversal": {root, traversal, {Done: true}},
		"missing parent": {root, missingParent, {Done: true}}, "cycle": {root, cycle, {Done: true}},
		"after completion": {root, {Done: true}, file}, "missing root": {{Done: true}},
	}
	for name, records := range cases {
		t.Run(name, func(t *testing.T) {
			var input strings.Builder
			for _, r := range records {
				b, _ := json.Marshal(r)
				input.Write(b)
				input.WriteByte('\n')
			}
			_, err := readScanManifest(strings.NewReader(input.String()), []FileItem{{Path: "/tree", Dir: true}}, map[string]Attr{"/tree": {Mode: 0040700}}, nil)
			if err == nil {
				t.Fatal("accepted invalid manifest")
			}
		})
	}
}

func TestScanHelperRejectsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode 000")
	}
	s := localSFTP(t)
	pythonScan(t, s)
	root := t.TempDir()
	file := filepath.Join(root, "unreadable")
	if err := os.WriteFile(file, []byte("private"), 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(file, 0600)
	a, err := s.Stat(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.scanWithHelper([]FileItem{{Path: root, Dir: true}}, map[string]Attr{root: a}, nil); err == nil {
		t.Fatal("unreadable source accepted")
	}
}

func TestScanManifestPreservesBytePaths(t *testing.T) {
	name := string([]byte{'/', 'f', 0xff, '\n'})
	b, _ := json.Marshal(scanRecord{Path: base64.StdEncoding.EncodeToString([]byte(name)), Mode: 0100600, Size: 7})
	result, err := readScanManifest(strings.NewReader(string(b)+"\n{\"done\":true}\n"), []FileItem{{Path: name}}, map[string]Attr{name: {Mode: 0100600, Size: 7}}, nil)
	if err != nil || result.attrs[name].Size != 7 {
		t.Fatal("byte path changed", err)
	}
}

func TestScanHelperDownload(t *testing.T) {
	s := localSFTP(t)
	pythonScan(t, s)
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	source := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0750); err != nil {
		t.Fatal(err)
	}
	contents := map[string]string{"nested/file": "test payload", ".hidden": "hidden", "empty": ""}
	for name, body := range contents {
		if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0640); err != nil {
			t.Fatal(err)
		}
	}
	destination := t.TempDir()
	p, err := planBatch(s, []FileItem{{Path: source}}, destination, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = executeBatchPlan(s, Host{Target: "fixture"}, p, true, Options{Yes: true}, nil); err != nil {
		t.Fatal(err)
	}
	for name, want := range contents {
		got, err := os.ReadFile(filepath.Join(destination, "tree", name))
		if err != nil || string(got) != want {
			t.Fatal("download mismatch", name, err)
		}
	}
	if _, err = planBatch(s, []FileItem{{Path: source}}, destination, true, false); err == nil {
		t.Fatal("helper bypassed overwrite protection")
	}
}
