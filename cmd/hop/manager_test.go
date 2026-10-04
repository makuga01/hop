package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func managerFixture() *dualManager {
	d := newManager("/local", "/remote", Host{Target: "test"}, nil, SortOrder{})
	for i, m := range d.panes {
		root := []string{"/local", "/remote"}[i]
		m.items = []FileItem{{Name: "bundle", Path: root + "/bundle", Dir: true}, {Name: "alpha.txt", Path: root + "/alpha.txt", Regular: true}, {Name: "beta.txt", Path: root + "/beta.txt", Regular: true}}
	}
	return d
}
func TestManagerSelectionAndGeometry(t *testing.T) {
	for _, side := range []int{0, 1} {
		d := managerFixture()
		d.active = side
		m := d.panes[side]
		d.toggleFocused()
		if m.cursor != 0 || len(m.marks) != 1 {
			t.Fatal("Space advanced cursor")
		}
		d.toggleFocused()
		if m.cursor != 0 || len(m.marks) != 0 {
			t.Fatal("Space did not unmark same folder")
		}
		d.toggleFocused()
		a := d.selectedAction()
		if a.side != side || len(a.files) != 1 || !a.files[0].Dir {
			t.Fatal(a)
		}
	}
	for _, width := range []int{64, 80, 120, 200} {
		d := managerFixture()
		l := d.layout(width, 28)
		d.mouse(mouseEvent{button: 2, x: l.right + 5, y: 10}, width, 28)
		if d.active != 1 || len(d.panes[1].marks) != 0 || d.panes[1].cursor != 0 {
			t.Fatal("focus click acted on a file", width)
		}
		d.mouse(mouseEvent{button: 2, x: l.right + 5, y: 10}, width, 28)
		if d.active != 1 || len(d.panes[1].marks) != 1 || d.panes[1].cursor != 1 {
			t.Fatal("right pane click mismatch", width)
		}
		d.mouse(mouseEvent{button: 65, x: l.right + 5, y: 10}, width, 28)
		if d.panes[1].cursor != 2 {
			t.Fatal("wheel did not move pointer")
		}
		output := regexp.MustCompile(`\[[0-9;?]*[a-zA-Z]`).ReplaceAllString(d.render(width, 28), "")
		lines := strings.Split(output, "\r\n")
		if len(lines) > 28 {
			t.Fatal("vertical overflow", width, len(lines))
		}
		for _, line := range lines {
			if textWidth(line) > width-1 {
				t.Fatal("horizontal overflow", width, textWidth(line), line)
			}
		}
	}
}
func TestManagerTransferBothDirections(t *testing.T) {
	oldState := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = oldState }()
	s := localSFTP(t)
	local, remote := t.TempDir(), t.TempDir()
	bundle := filepath.Join(local, "bundle")
	if e := os.MkdirAll(filepath.Join(bundle, "empty"), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(bundle, "tiny.txt"), []byte("hello"), 0644); e != nil {
		t.Fatal(e)
	}
	d := newManager(local, remote, Host{Target: "manager-test"}, s, SortOrder{})
	f := FileItem{Name: "bundle", Path: bundle, Dir: true}
	d.panes[0].toggle(f)
	runManagerCopyTest(d, managerAction{side: 0, files: []FileItem{f}})
	if !strings.HasPrefix(d.notice, "Copied") {
		t.Fatal(d.notice)
	}
	got, e := os.ReadFile(filepath.Join(remote, "bundle", "tiny.txt"))
	if e != nil || string(got) != "hello" {
		t.Fatal(string(got), e)
	}
	if info, e := os.Stat(filepath.Join(remote, "bundle", "empty")); e != nil || !info.IsDir() {
		t.Fatal("empty folder lost")
	}
	downloads := t.TempDir()
	d.panes[0].current = downloads
	rf := FileItem{Name: "bundle", Path: filepath.Join(remote, "bundle"), Dir: true}
	d.panes[1].toggle(rf)
	runManagerCopyTest(d, managerAction{side: 1, files: []FileItem{rf}})
	if got, e := os.ReadFile(filepath.Join(downloads, "bundle", "tiny.txt")); e != nil || string(got) != "hello" {
		t.Fatal(string(got), e)
	}
	// Existing destinations are refused, retaining selection for correction.
	d.panes[1].toggle(rf)
	runManagerCopyTest(d, managerAction{side: 1, files: []FileItem{rf}})
	if !strings.HasPrefix(d.notice, "Copy failed:") || len(d.panes[1].marks) != 1 {
		t.Fatal("failed transfer lost marks", d.notice)
	}
}
func TestManagerTerminal(t *testing.T) {
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("python3 required")
	}
	out, e := exec.Command(python, "testdata/manager_terminal.py", os.Args[0]).CombinedOutput()
	if e != nil {
		t.Fatalf("manager PTY: %v\n%s", e, out)
	}
}
func TestManagerTerminalHelper(t *testing.T) {
	if os.Getenv("MANAGER_TERMINAL_TEST") != "1" {
		t.Skip("PTY helper")
	}
	oldState := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = oldState }()
	s := localSFTP(t)
	local, remote := t.TempDir(), t.TempDir()
	if e := os.WriteFile(filepath.Join(local, "alpha.txt"), []byte("hello"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(filepath.Join(local, "bundle"), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(local, "bundle", "inside.txt"), []byte("tiny"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(remote, "remote.txt"), []byte("remote"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(local, ".secret"), []byte("secret"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, root := range []string{local, remote} {
		if e := os.WriteFile(filepath.Join(root, "discard.txt"), []byte("discard"), 0644); e != nil {
			t.Fatal(e)
		}
	}
	for _, root := range []string{local, remote} {
		body := "original"
		if root == local {
			body = "replacement"
		}
		if e := os.WriteFile(filepath.Join(root, "replace.txt"), []byte(body), 0644); e != nil {
			t.Fatal(e)
		}
	}
	large, e := os.Create(filepath.Join(local, "large.bin"))
	if e != nil {
		t.Fatal(e)
	}
	if e = large.Truncate(256 * 1024 * 1024); e != nil {
		t.Fatal(e)
	}
	large.Close()
	screen := beginFullscreen()
	defer func() {
		if screen != nil {
			screen.Close()
		}
	}()
	d := newManager(local, remote, Host{Target: "fixture"}, s, SortOrder{})
	if e := os.Mkdir(filepath.Join(remote, "private"), 0755); e != nil {
		t.Fatal(e)
	}
	actualLoader := d.loaders[1]
	d.loaders[1] = func(p string) ([]FileItem, error) {
		if p == filepath.Join(remote, "private") {
			return nil, &StatusError{Code: 3, Message: "Permission denied"}
		}
		return actualLoader(p)
	}
	a, e := d.Run()
	if e != nil || a.kind != "quit" {
		t.Fatal(a, e)
	}
	if b, e := os.ReadFile(filepath.Join(remote, "alpha.txt")); e != nil || string(b) != "hello" {
		t.Fatal(e)
	}
	if b, e := os.ReadFile(filepath.Join(local, "remote.txt")); e != nil || string(b) != "remote" {
		t.Fatal(e)
	}
	if info, e := os.Stat(filepath.Join(local, "new folder")); e != nil || !info.IsDir() {
		t.Fatal("folder not created", e)
	}
	if info, e := os.Stat(filepath.Join(remote, "large.bin")); e != nil || info.Size() != 256*1024*1024 {
		t.Fatal("large copy did not complete without an extra confirmation", e)
	}
	for _, root := range []string{local, remote} {
		if _, e := os.Stat(filepath.Join(root, "discard.txt")); !os.IsNotExist(e) {
			t.Fatal("delete failed", root, e)
		}
	}
	if _, e := os.Stat(filepath.Join(local, "bundle")); !os.IsNotExist(e) {
		t.Fatal("move left source", e)
	}
	if b, e := os.ReadFile(filepath.Join(remote, "bundle/inside.txt")); e != nil || string(b) != "tiny" {
		t.Fatal("move lost contents", e)
	}
	if b, e := os.ReadFile(filepath.Join(remote, "replace.txt")); e != nil || string(b) != "replacement" {
		t.Fatal("GUI overwrite failed", e)
	}
	if _, e := os.Stat(filepath.Join(local, "replace.txt")); !os.IsNotExist(e) {
		t.Fatal("GUI move retained source", e)
	}
	screen.Close()
	screen = nil
	fmt.Println("MANAGER_OK")
}

func TestManagerConnectionSurvivesPickerHandoff(t *testing.T) {
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	server := localSFTP(t)
	cancelled := make(chan struct{})
	connect := func(ctx context.Context, _ Host) (*SFTP, error) {
		context.AfterFunc(ctx, func() { server.Abort(); close(cancelled) })
		return server, nil
	}
	s, home, closeConnection, e := managerConnectUsing(Host{Target: "lifetime-test"}, connect)
	if e != nil {
		t.Fatal(e)
	}
	defer closeConnection()
	if home == "" {
		t.Fatal("missing remote home")
	}
	if _, e = s.Realpath("."); e != nil {
		t.Fatal("connection was cancelled after picker handoff", e)
	}
	closeConnection()
	<-cancelled
}

func runManagerCopyTest(d *dualManager, a managerAction) {
	results := make(chan transferResult, 1)
	d.beginCopy(a, results)
	for d.transfer.busy() {
		d.copyResult(<-results, results)
	}
	if d.transfer.stage == "done" {
		d.notice = "Copied"
	} else {
		d.notice = "Copy failed: " + d.transfer.message
	}
}
