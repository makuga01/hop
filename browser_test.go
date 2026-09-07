package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestBrowserPermissionDeniedPreservesPositionAndMarks(t *testing.T) {
	a := FileItem{Name: "a.txt", Path: "/home/test/a.txt", Regular: true, Size: 1}
	denied := FileItem{Name: "private", Path: "/home/test/private", Dir: true}
	m := newBrowserModel(Browser{Current: "/home/test"})
	m.items = []FileItem{a, denied}
	m.cursor = 1
	m.toggle(a)
	m.loading = true
	m.apply(browserListing{Target: denied.Path, Err: &StatusError{Code: 3, Message: "Permission denied"}})
	if m.current != "/home/test" || m.cursor != 1 || len(m.items) != 2 || len(m.selected()) != 1 || m.loading || !strings.Contains(m.notice, "Permission denied") {
		t.Fatalf("browser lost state: %+v", m)
	}
	m.apply(browserListing{Target: "/home", Items: []FileItem{{Name: "test", Path: "/home/test", Dir: true}}})
	if m.current != "/home" || len(m.selected()) != 1 {
		t.Fatal("could not recover or lost marks")
	}
}
func TestBrowserMarksAcrossDirectoriesAndReview(t *testing.T) {
	m := newBrowserModel(Browser{Current: "/a"})
	a := FileItem{Name: "one", Path: "/a/one", Regular: true}
	b := FileItem{Name: "two", Path: "/b/two", Regular: true}
	m.toggle(a)
	m.apply(browserListing{Target: "/b", Items: []FileItem{b}})
	m.toggle(b)
	if !m.beginReview() || len(m.visible()) != 2 {
		t.Fatal("selection not retained")
	}
	m.toggle(a)
	m.toggle(a)
	got := m.selected()
	if len(got) != 2 || got[0].Path != b.Path || got[1].Path != a.Path {
		t.Fatal("selection order or duplicates", got)
	}
}
func TestBrowserRenderDimensionsAndSinglePane(t *testing.T) {
	b := Browser{Title: "Select files", Current: "/tmp"}
	m := newBrowserModel(b)
	m.items = []FileItem{{Name: "日本語.txt", Path: "/tmp/日本語.txt", Regular: true, Size: 1}, {Name: "more.txt", Path: "/tmp/more.txt", Regular: true}}
	m.toggle(m.items[0])
	ansi := regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]")
	for _, width := range []int{40, 80, 120} {
		output := ansi.ReplaceAllString(b.render(m, width, 24), "")
		for _, line := range strings.Split(output, "\r\n") {
			if textWidth(line) > width-1 {
				t.Fatalf("overflow at width %d: %q (%d)", width, line, textWidth(line))
			}
		}
		if strings.Count(output, "┌") != 2 || strings.Count(output, "│  Name") != 1 {
			t.Fatal("expected one file listing and a selection box")
		}
		if !strings.Contains(output, "1 marked") {
			t.Fatal("missing mark count")
		}
	}
}
func TestSelectionBoxLifecycleAndLayout(t *testing.T) {
	b := Browser{Title: "Select files", Current: "/home/marek"}
	m := newBrowserModel(b)
	ansi := regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]")
	render := func(w, h int) string { return ansi.ReplaceAllString(b.render(m, w, h), "") }
	if strings.Contains(render(80, 24), "Selected ·") {
		t.Fatal("empty selection box shown")
	}
	for _, p := range []string{"/home/marek/reports/notes.md", "/tmp/notes.md", "/home/marek/archive", "/home/marek/build.zip", "/home/marek/日本語.csv", "/home/marek/latest.txt"} {
		m.toggle(FileItem{Name: filepath.Base(p), Path: p, Regular: true, Size: 1200})
	}
	m.marks["/home/marek/archive"] = FileItem{Name: "archive", Path: "/home/marek/archive", Dir: true}
	m.apply(browserListing{Target: "/var/log", Items: []FileItem{{Name: "server.log", Path: "/var/log/server.log", Regular: true}}})
	for _, w := range []int{40, 80, 120} {
		for _, h := range []int{12, 16, 18, 24, 40} {
			output := render(w, h)
			if strings.Count(output, "\r\n") >= h {
				t.Fatalf("height overflow %dx%d", w, h)
			}
			for _, line := range strings.Split(output, "\r\n") {
				if textWidth(line) > w-1 {
					t.Fatalf("width overflow %dx%d: %q", w, h, line)
				}
			}
			if !strings.Contains(output, "latest.txt") {
				t.Fatal("latest mark hidden after navigation", w, h)
			}
			if h >= 16 && !strings.Contains(output, "Wheel to scroll") {
				t.Fatal("hidden items not indicated")
			}
		}
	}
	output := render(80, 24)
	if !strings.Contains(output, "archive/") || !strings.Contains(output, "<DIR>") {
		t.Fatal("folder missing from selection")
	}
	t.Log("Selection preview:\n" + output)
	b.FolderOnly = true
	if !strings.Contains(render(80, 24), "Selected · 6 items") {
		t.Fatal("destination lost selection")
	}
	b.FolderOnly = false
	m.beginReview()
	if strings.Count(render(80, 24), "┌") != 1 {
		t.Fatal("selection duplicated in review")
	}
	m.review = false
	for _, f := range m.selected() {
		m.toggle(f)
	}
	if strings.Contains(render(80, 24), "Selected ·") {
		t.Fatal("box remains after unmarking everything")
	}
	if got := strings.TrimSpace(fitEnd("/very/long/parent/directory/日本語.txt", 16)); !strings.HasSuffix(got, "日本語.txt") || textWidth(got) > 16 {
		t.Fatal("path truncation lost filename", got)
	}
}

func TestBrowserKeySequences(t *testing.T) {
	cases := map[string]string{"\t": "accept", "\x12": "recent", " ": "space", "\r": "enter", "\x1b[15~": "accept", "\x1bOQ": "recent", "\x14": "accept", "\x1b[D": "left"}
	for in, want := range cases {
		got, n := browserKey([]byte(in))
		if got != want || n != len(in) {
			t.Fatal(in, got, n)
		}
	}
}
func TestBatchMultiFileRoundTripAndPreflight(t *testing.T) {
	s := localSFTP(t)
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	root := t.TempDir()
	upload := filepath.Join(root, "remote")
	download := filepath.Join(root, "download")
	_ = os.Mkdir(upload, 0700)
	_ = os.Mkdir(download, 0700)
	files := []FileItem{}
	for _, name := range []string{"one.txt", "two space.txt"} {
		p := filepath.Join(root, name)
		_ = os.WriteFile(p, []byte("data "+name), 0600)
		files = append(files, FileItem{Name: name, Path: p, Regular: true})
	}
	h := Host{Target: "local-test"}
	if e := copyBatch(s, h, files, upload, false, Options{Yes: true}); e != nil {
		t.Fatal(e)
	}
	remoteFiles := []FileItem{}
	for _, f := range files {
		remoteFiles = append(remoteFiles, FileItem{Name: f.Name, Path: filepath.Join(upload, f.Name), Regular: true})
	}
	if e := copyBatch(s, h, remoteFiles, download, true, Options{Yes: true}); e != nil {
		t.Fatal(e)
	}
	for _, f := range files {
		original, _ := os.ReadFile(f.Path)
		copied, _ := os.ReadFile(filepath.Join(download, f.Name))
		if !reflect.DeepEqual(original, copied) {
			t.Fatal("batch corruption")
		}
	}
	newFile := filepath.Join(root, "new.txt")
	_ = os.WriteFile(newFile, []byte("new"), 0600)
	conflicting := []FileItem{{Name: "new.txt", Path: newFile, Regular: true}, files[0]}
	if e := copyBatch(s, h, conflicting, upload, false, Options{Yes: true}); e == nil {
		t.Fatal("missed conflict")
	}
	if _, e := os.Stat(filepath.Join(upload, "new.txt")); !os.IsNotExist(e) {
		t.Fatal("wrote first file before preflighting second conflict")
	}
	duplicate := append([]FileItem{}, files[0], files[0])
	if _, e := planBatch(s, duplicate, upload, false, true); e == nil {
		t.Fatal("duplicate basenames not caught")
	}
}
func TestFileBrowserTerminal(t *testing.T) {
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("python3 required")
	}
	cmd := exec.Command(python, "testdata/file_browser.py", os.Args[0])
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("file browser terminal: %v\n%s", e, out)
	}
}
func TestFileBrowserTerminalHelper(t *testing.T) {
	if os.Getenv("HOP_BROWSER_TEST") != "1" {
		t.Skip("PTY helper")
	}
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	root := t.TempDir()
	src := filepath.Join(root, "files")
	dest := filepath.Join(root, "destination")
	_ = os.Mkdir(src, 0700)
	_ = os.Mkdir(dest, 0700)
	for _, name := range []string{"alpha.txt", "beta.txt"} {
		_ = os.WriteFile(filepath.Join(src, name), []byte(name), 0600)
	}
	bundle := filepath.Join(src, "bundle")
	_ = os.MkdirAll(filepath.Join(bundle, "empty"), 0700)
	_ = os.WriteFile(filepath.Join(bundle, "nested.txt"), []byte("nested"), 0600)
	denied := filepath.Join(src, "private")
	_ = os.Mkdir(denied, 0000)
	defer os.Chmod(denied, 0700)
	s := localSFTP(t)
	screen := beginFullscreen()
	if screen == nil {
		t.Fatal("fullscreen session unavailable")
	}
	defer func() { screen.Close(); println("FULLSCREEN_CLOSED") }()
	result, e := (Browser{Title: "BROWSER_TEST", Current: src, Home: src, Load: remoteFileLoader(s), Recent: []FileItem{{Name: "source", Path: src, Dir: true}}}).Run()
	if e != nil {
		t.Fatal(e)
	}
	if len(result.Files) != 3 {
		t.Fatal("expected two files and a folder", result)
	}
	connecting := startActivity("Connecting to test server", 0, false)
	answer, err := prompt("AUTH_TEST > ")
	connecting.Stop()
	if err != nil || answer != "ready" {
		t.Fatal("loading screen stole prompt input", answer, err)
	}
	if e = os.Mkdir(filepath.Join(dest, "existing"), 0755); e != nil {
		t.Fatal(e)
	}
	destination, e := (Browser{Title: "DESTINATION_TEST", Current: dest, Home: dest, FolderOnly: true, Mkdir: func(p string) error { time.Sleep(250 * time.Millisecond); return s.Mkdir(p, 0755) }, Load: remoteFileLoader(s), Preselected: result.Files}).Run()
	if e != nil || destination.Directory != filepath.Join(dest, "new folder") {
		t.Fatal("Tab did not select destination", destination, e)
	}
	dest = destination.Directory
	if e = copyBatch(s, Host{Target: "local-test"}, result.Files, destination.Directory, true, Options{Yes: true}); e != nil {
		t.Fatal(e)
	}
	for _, f := range result.Files {
		if f.Dir {
			got, err := os.ReadFile(filepath.Join(dest, f.Name, "nested.txt"))
			if err != nil || string(got) != "nested" {
				t.Fatal("nested download mismatch", err)
			}
			if a, err := os.Stat(filepath.Join(dest, f.Name, "empty")); err != nil || !a.IsDir() {
				t.Fatal("missing empty folder", err)
			}
			continue
		}
		got, _ := os.ReadFile(filepath.Join(dest, f.Name))
		if string(got) != f.Name {
			t.Fatal("download mismatch")
		}
	}
	println("BROWSER_BATCH_OK")
}
