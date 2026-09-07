package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func parseLine(t *testing.T, line string) (Host, []string, bool) {
	t.Helper()
	w, e := shellWords(stripHistory(line))
	if e != nil {
		return Host{}, nil, false
	}
	return parseConnection(w)
}
func TestHistoryConnections(t *testing.T) {
	tests := []struct {
		line, target string
		opts         []string
		ok           bool
	}{
		{`ssh prod`, "prod", nil, true},
		{`: 1770000000:0;ssh -i '/tmp/work key' -p 2222 marek@203.0.113.42`, "marek@203.0.113.42", []string{"-i", "/tmp/work key", "-p", "2222"}, true},
		{`ssh -vv -J jump -l deploy prod 'ls /srv'`, "prod", []string{"-J", "jump", "-l", "deploy"}, true},
		{`scp -P 2222 '/tmp/a b' 'user@[2001:db8::1]:/srv/a b'`, "user@2001:db8::1", []string{"-p", "2222"}, true},
		{`sftp -P2200 user@host`, "user@host", []string{"-p", "2200"}, true},
		{`ssh ssh://user@host:2200`, "user@host", []string{"-p", "2200"}, true},
		{`ssh -o 'ProxyCommand=touch /tmp/should-not-exist' prod`, "", nil, false},
		{`ssh -o '' prod`, "", nil, false},
		{`ssh $(whoami)@host`, "", nil, false},
		{`ssh -i relative.pem prod`, "", nil, false},
		{`ssh -o StrictHostKeyChecking=no prod`, "", nil, false},
		{`echo ssh prod`, "", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			h, _, ok := parseLine(t, tt.line)
			if ok != tt.ok {
				t.Fatalf("ok %v, want %v: %+v", ok, tt.ok, h)
			}
			if ok && (h.Target != tt.target || !reflect.DeepEqual(h.Options, tt.opts)) {
				t.Fatalf("got %+v, want target %s options %v", h, tt.target, tt.opts)
			}
		})
	}
}
func TestRemoteHistoryDoesNotInventRelativePaths(t *testing.T) {
	cs := remoteHints("cd /srv/app\ncd ../unknown\ntail /srv/app/logs/a.log\nvim ~/notes.txt\ncat $(touch /bad)\n", "/home/test")
	paths := map[string]bool{}
	for _, c := range cs {
		paths[c.Path] = true
	}
	if !paths["/srv/app"] || !paths["/srv/app/logs/a.log"] || !paths["/home/test/notes.txt"] {
		t.Fatal(cs)
	}
	if paths["/srv/unknown"] || paths["/bad"] {
		t.Fatal("invented or executable path", cs)
	}
}
func TestConfigIncludes(t *testing.T) {
	d := t.TempDir()
	_ = os.MkdirAll(filepath.Join(d, ".ssh"), 0700)
	_ = os.WriteFile(filepath.Join(d, ".ssh", "config"), []byte("Host prod staging\nHost * !nope\nInclude extra.conf\n"), 0600)
	_ = os.WriteFile(filepath.Join(d, ".ssh", "extra.conf"), []byte("Host lab\nInclude config\n"), 0600)
	var got []string
	configHosts(filepath.Join(d, ".ssh", "config"), d, map[string]bool{}, 0, func(h Host) { got = append(got, h.Target) })
	if !reflect.DeepEqual(got, []string{"prod", "staging", "lab"}) {
		t.Fatal(got)
	}
}
func TestHostKeyAndOverrides(t *testing.T) {
	h := Host{Target: "prod"}
	if h.Key() != (Host{Target: "prod", Options: []string{}}).Key() {
		t.Fatal("nil/empty identity differs")
	}
	h.Options = []string{"-p", "22", "-i", "/tmp/key"}
	got := withOverrides(h, []string{"-p", "2200"})
	if !reflect.DeepEqual(got.Options, []string{"-p", "2200", "-i", "/tmp/key"}) {
		t.Fatal(got)
	}
}
func TestPickerFuzzyAndTerminalEscaping(t *testing.T) {
	if !fuzzy("/srv/app/releases", "srv rel") {
		t.Fatal("fuzzy mismatch")
	}
	if fuzzy("abc", "acb") {
		t.Fatal("bad ordering")
	}
	if strings.ContainsRune(safeText("x\x1b[31m\n"), 27) {
		t.Fatal("terminal injection")
	}
}
func TestStateConcurrentUpdates(t *testing.T) {
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if e := rememberHost(Host{Target: fmt.Sprint("host", i)}); e != nil {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	s, e := readState()
	if e != nil || len(s.Hosts) != 12 {
		t.Fatal(e, len(s.Hosts))
	}
	info, _ := os.Stat(filepath.Join(dataDir, "state.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}
func TestTrackingWriterSplitMarkers(t *testing.T) {
	var out bytes.Buffer
	var paths []string
	prefix := []byte("\x1b]777;hop;test;")
	w := trackingWriter{out: &out, prefix: prefix, onPath: func(s string) { paths = append(paths, s) }}
	_, _ = w.Write([]byte("prompt$ "))
	if out.String() != "prompt$ " {
		t.Fatal("prompt was buffered", out.String())
	}
	input := string(prefix) + base64.StdEncoding.EncodeToString([]byte("/tmp/space and 'quote'")) + "\aafter"
	for _, b := range []byte(input) {
		_, _ = w.Write([]byte{b})
	}
	w.Flush()
	if out.String() != "prompt$ after" || len(paths) != 1 || paths[0] != "/tmp/space and 'quote'" {
		t.Fatal(out.String(), paths)
	}
}
func TestTrackingBootstrap(t *testing.T) {
	// Replace only the user's rc-file source in this test; never read their shell config.
	command := trackingCommand("abc123")
	command = strings.Replace(command, "[[ -f ~/.bashrc ]] && source ~/.bashrc", "PROMPT_COMMAND='true'", 1)
	cmd := exec.Command("/bin/bash", "-c", command)
	cmd.Stdin = strings.NewReader("cd /tmp\nprintf 'ready\\n'\nexit\n")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if e := cmd.Run(); e != nil {
		t.Fatal(e, out.String())
	}
	if !strings.Contains(out.String(), base64.StdEncoding.EncodeToString([]byte("/tmp"))) {
		t.Fatal("cwd was not reported", out.String())
	}
}

func localSFTP(t *testing.T) *SFTP {
	t.Helper()
	server := "/usr/libexec/sftp-server"
	if _, e := os.Stat(server); e != nil {
		server = "/usr/lib/openssh/sftp-server"
		if _, e = os.Stat(server); e != nil {
			t.Skip("OpenSSH sftp-server not installed")
		}
	}
	cmd := exec.Command(server, "-e")
	in, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	s := &SFTP{in: in, out: out, cmd: cmd, ext: map[string]string{}, timeout: 5 * time.Second}
	t.Cleanup(s.Close)
	if e = s.packet(fxInit, u32(3)); e != nil {
		t.Fatal(e)
	}
	typ, b, e := s.receive()
	if e != nil || typ != fxVersion {
		t.Fatal(typ, e)
	}
	d := decoder{b: b}
	if d.u32() != 3 {
		t.Fatal("version")
	}
	for d.remaining() > 0 && d.err == nil {
		k := d.str()
		s.ext[k] = d.str()
	}
	if d.err != nil {
		t.Fatal(d.err)
	}
	return s
}
func TestSFTPRoundTripAndNoClobber(t *testing.T) {
	s := localSFTP(t)
	dir := t.TempDir()
	payload := bytes.Repeat([]byte("hello\x00world\n"), 160000)
	local := filepath.Join(dir, "source")
	if e := os.WriteFile(local, payload, 0600); e != nil {
		t.Fatal(e)
	}
	f, _ := os.Open(local)
	defer f.Close()
	remote := filepath.Join(dir, "spaces 'quotes' $HOME `literal` ü\nfile")
	var progress uint64
	if e := s.Upload(f, remote, uint64(len(payload)), 0600, func(n uint64) { progress = n }); e != nil {
		t.Fatal(e)
	}
	if progress != uint64(len(payload)) {
		t.Fatal(progress)
	}
	entries, e := s.ReadDir(dir)
	if e != nil || len(entries) != 2 {
		t.Fatal(entries, e)
	}
	a, e := s.Stat(remote, true)
	if e != nil || a.Size != uint64(len(payload)) || !a.Regular() {
		t.Fatal(a, e)
	}
	f2, e := os.Create(filepath.Join(dir, "download"))
	if e != nil {
		t.Fatal(e)
	}
	defer f2.Close()
	if e = s.Download(remote, f2, a.Size, func(uint64) {}); e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(f2.Name())
	if e != nil || !bytes.Equal(got, payload) {
		t.Fatal("round trip mismatch", e)
	}
	if e = s.Rename(remote, local, false); e == nil {
		t.Fatal("clobbered destination")
	}
	if e = s.Rename(remote, local, true); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Stat(remote, true); !noSuch(e) {
		t.Fatal("old name remains", e)
	}
}
func TestSFTPZeroByteAndHistoryTail(t *testing.T) {
	s := localSFTP(t)
	dir := t.TempDir()
	f, e := os.Create(filepath.Join(dir, "empty-local"))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	remote := filepath.Join(dir, "empty-remote")
	if e = s.Upload(f, remote, 0, 0600, func(uint64) {}); e != nil {
		t.Fatal(e)
	}
	a, e := s.Stat(remote, true)
	if e != nil || a.Size != 0 {
		t.Fatal(a, e)
	}
	history := filepath.Join(dir, "history")
	_ = os.WriteFile(history, []byte(strings.Repeat("old command\n", 100)+"cd /srv/app\n"), 0600)
	tail, e := s.Tail(history, 24)
	if e != nil || !strings.Contains(tail, "cd /srv/app") {
		t.Fatal(tail, e)
	}
}
func TestTransferRefusesOverwriteAndSymlink(t *testing.T) {
	s := localSFTP(t)
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	dest := filepath.Join(dir, "dest.txt")
	_ = os.WriteFile(src, []byte("new"), 0600)
	_ = os.WriteFile(dest, []byte("old"), 0600)
	h := Host{Target: "local-test"}
	if e := sendFile(s, h, src, dest, Options{Yes: true}); e == nil {
		t.Fatal("allowed overwrite")
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "old" {
		t.Fatal("modified old file")
	}
	if e := sendFile(s, h, src, dest, Options{Yes: true, Overwrite: true}); e != nil {
		t.Fatal(e)
	}
	got, _ = os.ReadFile(dest)
	if string(got) != "new" {
		t.Fatal("overwrite failed")
	}
	link := filepath.Join(dir, "link")
	_ = os.Symlink(dest, link)
	if e := getFile(s, h, src, link, Options{Yes: true, Overwrite: true}); e == nil {
		t.Fatal("overwrote symlink")
	}
	download := filepath.Join(dir, "fetched.txt")
	if e := getFile(s, h, src, download, Options{Yes: true}); e != nil {
		t.Fatal(e)
	}
	got, _ = os.ReadFile(download)
	if string(got) != "new" {
		t.Fatal("download failed")
	}
	dry := filepath.Join(dir, "dry.txt")
	if e := sendFile(s, h, src, dry, Options{Yes: true, DryRun: true}); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(dry); !os.IsNotExist(e) {
		t.Fatal("dry run wrote file")
	}
}

func TestRecentHistoryBeatsOldHopUse(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "history")
	_ = os.WriteFile(file, []byte(": 200:0;ssh newer\n: 300:0;ssh newest\n"), 0600)
	d := discoverInputs(State{Hosts: []Host{{Target: "old", LastUsed: 100}}}, dir, []string{file}, nil)
	if len(d.Hosts) != 3 || d.Hosts[0].Target != "newest" {
		t.Fatal(d.Hosts)
	}
}
func TestDownloadOutOfOrderReplies(t *testing.T) {
	toServerR, toServerW := io.Pipe()
	fromServerR, fromServerW := io.Pipe()
	defer toServerR.Close()
	defer toServerW.Close()
	defer fromServerR.Close()
	defer fromServerW.Close()
	s := &SFTP{in: toServerW, out: fromServerR, timeout: 2 * time.Second}
	server := &SFTP{in: fromServerW, out: toServerR}
	payload := bytes.Repeat([]byte("0123456789abcdef"), 65536)
	done := make(chan error, 1)
	go func() {
		for batch := 0; batch < 2; batch++ {
			type req struct {
				id  uint32
				off uint64
				n   uint32
			}
			var rs []req
			for i := 0; i < 16; i++ {
				typ, b, e := server.receive()
				if e != nil {
					done <- e
					return
				}
				if typ != fxRead {
					done <- fmt.Errorf("wrong packet %d", typ)
					return
				}
				d := decoder{b: b}
				id := d.u32()
				d.str()
				rs = append(rs, req{id, d.u64(), d.u32()})
			}
			for i := len(rs) - 1; i >= 0; i-- {
				r := rs[i]
				if e := server.packet(fxData, fields(u32(r.id), str(string(payload[r.off:r.off+uint64(r.n)])))); e != nil {
					done <- e
					return
				}
			}
		}
		done <- nil
	}()
	f, e := os.CreateTemp(t.TempDir(), "download")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if e = s.downloadData("handle", f, uint64(len(payload)), func(uint64) {}); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	got, _ := os.ReadFile(f.Name())
	if !bytes.Equal(got, payload) {
		t.Fatal("out-of-order responses corrupted file")
	}
}
func TestInterruptedDownloadLeavesDestinationUnchanged(t *testing.T) {
	s := localSFTP(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	dest := filepath.Join(dir, "dest")
	payload := bytes.Repeat([]byte("x"), 2<<20)
	_ = os.WriteFile(source, payload, 0600)
	_ = os.WriteFile(dest, []byte("original"), 0600)
	f, e := os.CreateTemp(dir, ".hop-*.partial")
	if e != nil {
		t.Fatal(e)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	e = s.Download(source, f, uint64(len(payload)), func(uint64) { s.Abort() })
	if e == nil {
		t.Fatal("interruption was ignored")
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "original" {
		t.Fatal("destination was modified")
	}
}

func TestCompactPickerItems(t *testing.T) {
	cs := []Candidate{{Path: "/tmp", Dir: true, Reason: "remote history: cd (inferred)", Score: 10}, {Path: "/tmp/a", Reason: "remote history: cat (inferred)", Size: 1}}
	items := candidateItems(cs, true)
	if len(items) != 2 || items[0].Detail != "" || items[1].Detail != "1 B" {
		t.Fatal(items)
	}
	if strings.Contains(hostDetail(Host{Target: "aisa", Source: "SSH history", Options: []string{"-p", "22"}}), "history") {
		t.Fatal("host source still visible")
	}
}
func TestConfirmationDefaultAndExplicitCancel(t *testing.T) {
	for _, input := range []string{"", "y", "Y", "yes", " YES "} {
		accept, valid := confirmationAnswer(input)
		if !accept || !valid {
			t.Fatalf("did not accept %q", input)
		}
	}
	for _, input := range []string{"n", "N", "no"} {
		accept, valid := confirmationAnswer(input)
		if accept || !valid {
			t.Fatalf("did not cancel %q", input)
		}
	}
	if _, valid := confirmationAnswer("typo"); valid {
		t.Fatal("invalid input silently decided")
	}
}
func TestActivityStartsBeforeProgressAndKeepsWaitingVisible(t *testing.T) {
	var out bytes.Buffer
	a := newActivity(&out, false, false, "Uploading", 1, true)
	a.mu.Lock()
	initial := out.String()
	msg := a.text(a.start.Add(4*time.Second), 1)
	a.mu.Unlock()
	if !strings.Contains(initial, "Uploading · 0% · 0 B / 1 B") || !strings.Contains(msg, "waiting…") {
		a.Stop()
		t.Fatal(initial, msg)
	}
	a.Update(1)
	a.Phase("Finalizing upload")
	a.Stop()
	a.Stop()
	if !strings.Contains(out.String(), "Finalizing upload · 100% · 1 B / 1 B") {
		t.Fatal(out.String())
	}
}

func TestTerminalHandoff(t *testing.T) {
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("python3 is needed for the PTY regression test")
	}
	cmd := exec.Command(python, "testdata/terminal_handoff.py", os.Args[0])
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("terminal regression: %v\n%s", e, out)
	}
}
func TestTerminalHandoffHelper(t *testing.T) {
	if os.Getenv("HOP_TERMINAL_TEST") != "1" {
		t.Skip("PTY subprocess helper")
	}
	dataDir = t.TempDir()
	dir := t.TempDir()
	src := filepath.Join(dir, "test")
	_ = os.WriteFile(src, []byte("x"), 0600)
	server := localSFTP(t)
	_, e := (Picker{Title: "Machine", Items: []PickItem{{Label: "aisa", Value: "aisa"}}}).Run()
	if e != nil {
		t.Fatal(e)
	}
	_, e = (Picker{Title: "Upload destination", Paths: true, Items: candidateItems([]Candidate{{Path: dir, Dir: true, Reason: "remote history: cd (inferred)"}}, false)}).Run()
	if e != nil {
		t.Fatal(e)
	}
	dest := filepath.Join(dir, "remote-test")
	if e = sendFile(server, Host{Target: "aisa"}, src, dest, Options{}); e != nil {
		t.Fatal(e)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "x" {
		t.Fatal("upload mismatch")
	}
	fmt.Println("ONE_BYTE_UPLOAD_OK")
	_, e = (Picker{Title: "Download source", Paths: true, Get: true, Items: []PickItem{{Label: dest, Value: dest}}}).Run()
	if e != nil {
		t.Fatal(e)
	}
	if e = getFile(server, Host{Target: "aisa"}, dest, filepath.Join(dir, "download"), Options{}); e != nil {
		t.Fatal(e)
	}
	fmt.Println("ENTER_DOWNLOAD_OK")
	if e = confirm("Cancel test?"); e != errCancelled {
		t.Fatal("n did not cancel", e)
	}
	value, e := prompt("Destination path: ")
	if e != nil || value != "/tmp/a b" {
		t.Fatal("prompt lost input", value, e)
	}
	busy := startActivity("Waiting test", 1, true)
	time.Sleep(650 * time.Millisecond)
	busy.Stop()
	fmt.Println("TERMINAL_HANDOFF_OK")
}
